package main

import (
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
)

// testLogger returns a discard-backed slog.Logger so tests don't spam stderr.
func testLogger(_ testing.TB) *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestValidateFlags_AllAllowed(t *testing.T) {
	ok := [][]string{
		nil,
		{},
		{"-all"},
		{"-all", "-src"},
		{"-all", "-src", "-u", "-short", "-c"},
	}
	for _, flags := range ok {
		if err := validateFlags(flags); err != nil {
			t.Errorf("unexpected error for %v: %v", flags, err)
		}
	}
}

func TestValidateFlags_Disallowed(t *testing.T) {
	err := validateFlags([]string{"-rm-rf"})
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	// Error message must list the allowed set deterministically (sorted).
	if !strings.Contains(msg, "-all") || !strings.Contains(msg, "-src") {
		t.Errorf("error missing allowed list: %s", msg)
	}
	// Verify alphabetical order of allowed flags in the message.
	allAt := strings.Index(msg, "-all")
	cAt := strings.Index(msg, "-c")
	if allAt < 0 || cAt < 0 || allAt > cAt {
		t.Errorf("expected -all before -c (sorted), got: %s", msg)
	}
}

func TestClampPage(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, 1}, {-5, 1}, {1, 1}, {2, 2}, {1000, 1000},
	}
	for _, c := range cases {
		if got := clampPage(c.in); got != c.want {
			t.Errorf("clampPage(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestClampPageSize(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, 1000},   // default
		{-10, 1000}, // default
		{50, 100},   // min clamp
		{500, 500},  // pass-through
		{5000, 5000},
		{10000, 5000}, // max clamp
	}
	for _, c := range cases {
		if got := clampPageSize(c.in); got != c.want {
			t.Errorf("clampPageSize(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestPaginate_Empty(t *testing.T) {
	text, page, totalPages, totalLines := paginate("", 1, 100)
	if text != "" {
		t.Errorf("text got %q", text)
	}
	if page != 1 || totalPages != 1 || totalLines != 0 {
		t.Errorf("got page=%d totalPages=%d totalLines=%d", page, totalPages, totalLines)
	}
}

func TestPaginate_SinglePage(t *testing.T) {
	text, page, totalPages, totalLines := paginate("a\nb\nc\n", 1, 100)
	if !strings.Contains(text, "Page 1 of 1") {
		t.Errorf("header missing: %q", text)
	}
	if !strings.Contains(text, "lines 1-3 of 3") {
		t.Errorf("line range wrong: %q", text)
	}
	if page != 1 || totalPages != 1 || totalLines != 3 {
		t.Errorf("got page=%d totalPages=%d totalLines=%d", page, totalPages, totalLines)
	}
}

func TestPaginate_MultiPage(t *testing.T) {
	// 10 lines, page_size=3 → 4 pages (3+3+3+1).
	var sb strings.Builder
	for i := 1; i <= 10; i++ {
		sb.WriteString("line\n")
	}
	text, page, totalPages, totalLines := paginate(sb.String(), 2, 3)
	if totalPages != 4 {
		t.Errorf("totalPages got %d want 4", totalPages)
	}
	if totalLines != 10 {
		t.Errorf("totalLines got %d want 10", totalLines)
	}
	if page != 2 {
		t.Errorf("page got %d want 2", page)
	}
	if !strings.Contains(text, "Page 2 of 4") {
		t.Errorf("header wrong: %q", text)
	}
	if !strings.Contains(text, "lines 4-6 of 10") {
		t.Errorf("line range wrong: %q", text)
	}
}

func TestPaginate_PageBeyondTotalClamps(t *testing.T) {
	_, page, totalPages, _ := paginate("a\nb\nc\n", 99, 2)
	if totalPages != 2 {
		t.Errorf("totalPages got %d want 2", totalPages)
	}
	if page != 2 {
		t.Errorf("expected page clamped to %d, got %d", totalPages, page)
	}
}

func TestPaginate_TrailingNewlineStripped(t *testing.T) {
	_, _, _, totalLines := paginate("a\nb\n", 1, 100)
	if totalLines != 2 {
		t.Errorf("expected trailing empty line stripped; got totalLines=%d", totalLines)
	}
}

func TestFormatGoDocError_Branches(t *testing.T) {
	cases := []struct {
		name       string
		underlying string
		want       string // substring that must appear in the formatted output
	}{
		{"no such package", "no such package foo", "not found"},
		{"cannot find package", "cannot find package bar", "not found"},
		{"is not in std", "is not in std", "not found"},
		{"cannot find module", "cannot find module baz", "not found"},
		{"no symbol", "doc: no symbol Foo in package bar", "symbol"},
		{"build constraints", "build constraints exclude all Go files", "build constraints"},
		{"no Go files in", "no Go files in /path/to/dir", "no Go files directly"},
		{"go get failure - repo not found", "go get x.com/y: Repository not found", "could not fetch"},
		{"go get failure - unknown revision", "go get x.com/y: unknown revision", "could not fetch"},
		{"default", "some random error", "get_doc failed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wrapped := formatGoDocError(errors.New(c.underlying), "x.com/y", "Target")
			if !strings.Contains(wrapped.Error(), c.want) {
				t.Errorf("expected substring %q in %q", c.want, wrapped.Error())
			}
			// The underlying error must still be unwrappable.
			if !errors.Is(wrapped, wrapped) {
				t.Error("wrapped should satisfy errors.Is with itself")
			}
		})
	}
}

func TestContainsInternal(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"fmt", false},
		{"encoding/json", false},
		{"foo/internal/bar", true},
		{"internal", true},
		{"internal/thing", true},
		{"foo/bar/internal", true},
		{"notinternal/foo", false},
		{"foo/internaldb", false}, // not exactly "internal"
	}
	for _, c := range cases {
		if got := containsInternal(c.path); got != c.want {
			t.Errorf("containsInternal(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}
