package main

import (
	"strings"
	"testing"
)

func TestSanitizeName_Basic(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"fmt", "fmt"},
		{"github.com/foo/bar", "github_com_foo_bar"},
		{"gopkg.in/yaml.v3", "gopkg_in_yaml_v3"},
		{"abc-def_ghi", "abc-def_ghi"},
		{"with spaces", "with_spaces"},
	}
	for _, c := range cases {
		if got := sanitizeName(c.in); got != c.want {
			t.Errorf("sanitizeName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSanitizeName_EmptyFallsBackToPkg(t *testing.T) {
	if got := sanitizeName(""); got != "pkg" {
		t.Errorf("sanitizeName(\"\") = %q, want \"pkg\"", got)
	}
}

func TestSanitizeName_AllPunctuation(t *testing.T) {
	// No alnum/-/_ characters — every byte sanitizes to '_', so the result
	// is a run of underscores, not the "pkg" fallback.
	got := sanitizeName("///...")
	if got == "pkg" {
		t.Errorf("expected underscores, got pkg fallback")
	}
	if strings.ContainsAny(got, "./") {
		t.Errorf("expected no raw punctuation, got %q", got)
	}
}

func TestSanitizeName_Truncation(t *testing.T) {
	long := strings.Repeat("a", 100)
	got := sanitizeName(long)
	if len(got) != 40 {
		t.Errorf("expected truncation to 40, got len=%d", len(got))
	}
}
