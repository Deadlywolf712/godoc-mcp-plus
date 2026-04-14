package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newResolver() *Resolver {
	return NewResolver(testLogger(nil))
}

func TestResolve_EmptyPath(t *testing.T) {
	_, err := newResolver().Resolve(context.Background(), "", "")
	if err == nil {
		t.Fatal("expected error on empty path")
	}
}

func TestResolve_Stdlib(t *testing.T) {
	cases := []string{"fmt", "encoding/json", "net/http"}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			r, err := newResolver().Resolve(context.Background(), p, "")
			if err != nil {
				t.Fatal(err)
			}
			if !r.IsStdlib {
				t.Errorf("expected IsStdlib for %q", p)
			}
			if r.ImportPath != p {
				t.Errorf("ImportPath mismatch: got %q", r.ImportPath)
			}
			if r.IsLocal {
				t.Error("stdlib should not be local")
			}
		})
	}
}

func TestResolve_External(t *testing.T) {
	r, err := newResolver().Resolve(context.Background(), "github.com/foo/bar", "")
	if err != nil {
		t.Fatal(err)
	}
	if r.IsStdlib {
		t.Error("github.com/... should not be stdlib")
	}
	if r.IsLocal {
		t.Error("github.com/... should not be local")
	}
	if r.ImportPath != "github.com/foo/bar" {
		t.Errorf("got %q", r.ImportPath)
	}
}

func TestResolve_RelativeRequiresWorkingDir(t *testing.T) {
	_, err := newResolver().Resolve(context.Background(), ".", "")
	if err == nil {
		t.Fatal("expected error when working_dir missing")
	}
}

func TestResolve_RelativeRequiresAbsoluteWorkingDir(t *testing.T) {
	_, err := newResolver().Resolve(context.Background(), ".", "relative/path")
	if err == nil {
		t.Fatal("expected error on non-absolute working_dir")
	}
}

func TestResolve_RelativeDotResolvesToModuleRoot(t *testing.T) {
	dir := writeGoMod(t, "example.com/mymod")
	r, err := newResolver().Resolve(context.Background(), ".", dir)
	if err != nil {
		t.Fatal(err)
	}
	if r.ImportPath != "example.com/mymod" {
		t.Errorf("got %q", r.ImportPath)
	}
	if !r.IsLocal {
		t.Error("expected IsLocal")
	}
	if r.WorkingDir != dir {
		t.Errorf("WorkingDir got %q want %q", r.WorkingDir, dir)
	}
}

func TestResolve_RelativeSubDirJoinsModulePath(t *testing.T) {
	dir := writeGoMod(t, "example.com/mymod")
	r, err := newResolver().Resolve(context.Background(), "./sub/pkg", dir)
	if err != nil {
		t.Fatal(err)
	}
	if r.ImportPath != "example.com/mymod/sub/pkg" {
		t.Errorf("got %q", r.ImportPath)
	}
}

func TestResolve_RelativeEscapeRejected(t *testing.T) {
	dir := writeGoMod(t, "example.com/mymod")
	for _, p := range []string{"..", "../foo", "../../bar"} {
		t.Run(p, func(t *testing.T) {
			_, err := newResolver().Resolve(context.Background(), p, dir)
			if err == nil {
				t.Fatalf("expected error for %q", p)
			}
			if !strings.Contains(err.Error(), "escapes module root") {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestResolve_AbsolutePath(t *testing.T) {
	dir := writeGoMod(t, "example.com/mymod")
	r, err := newResolver().Resolve(context.Background(), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if r.ImportPath != "example.com/mymod" {
		t.Errorf("got %q", r.ImportPath)
	}
	if !r.IsLocal {
		t.Error("expected IsLocal")
	}
}

func TestResolve_AbsolutePathConflictingWorkingDir(t *testing.T) {
	dir := writeGoMod(t, "example.com/mymod")
	other := t.TempDir()
	_, err := newResolver().Resolve(context.Background(), dir, other)
	if err == nil {
		t.Fatal("expected error on conflicting working_dir")
	}
}

func TestReadModuleName_Valid(t *testing.T) {
	dir := writeGoMod(t, "example.com/foo")
	got, err := readModuleName(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != "example.com/foo" {
		t.Errorf("got %q", got)
	}
}

func TestReadModuleName_MissingFile(t *testing.T) {
	if _, err := readModuleName(t.TempDir()); err == nil {
		t.Fatal("expected error on missing go.mod")
	}
}

func TestReadModuleName_NoModuleLine(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("go 1.26\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readModuleName(dir); err == nil {
		t.Fatal("expected error when no module line")
	}
}

func TestReadModuleName_TrailingComment(t *testing.T) {
	dir := t.TempDir()
	content := "module example.com/foo // a comment\n\ngo 1.26\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := readModuleName(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != "example.com/foo" {
		t.Errorf("got %q", got)
	}
}

func TestReadModuleName_SkipsLeadingCommentsAndBlanks(t *testing.T) {
	dir := t.TempDir()
	content := "// header comment\n\n// another\nmodule example.com/bar\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := readModuleName(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != "example.com/bar" {
		t.Errorf("got %q", got)
	}
}

func writeGoMod(t *testing.T, modulePath string) string {
	t.Helper()
	dir := t.TempDir()
	content := "module " + modulePath + "\n\ngo 1.26\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return dir
}
