package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// ResolvedPath is a package path resolved to its canonical import path and
// the directory that should be used as the working directory for any 'go doc'
// or 'go list' subprocess.
type ResolvedPath struct {
	// ImportPath is the canonical import path (e.g. "fmt" or
	// "github.com/foo/bar/sub").
	ImportPath string

	// WorkingDir is the directory from which 'go doc' / 'go list' should be
	// invoked to resolve this package. Empty string means the sandbox will
	// supply one (stdlib or fetched external package).
	WorkingDir string

	// IsStdlib is true when ImportPath is in the Go standard library.
	IsStdlib bool

	// IsLocal is true when the path was resolved against a caller-supplied
	// working directory (relative or absolute filesystem path).
	IsLocal bool
}

// Resolver turns a user-supplied path + optional working_dir into a
// ResolvedPath that downstream components can act on.
type Resolver struct {
	logger *slog.Logger
}

func NewResolver(logger *slog.Logger) *Resolver {
	return &Resolver{logger: logger}
}

// Resolve classifies the input path and returns a ResolvedPath.
//
// Rules:
//   - Starts with "." or ".." → relative local path; requires workingDir to be
//     the module root (where go.mod lives). Paths that escape the module root
//     via ".." are rejected.
//   - Absolute filesystem path → local path; workingDir must match if given.
//   - "stdlib-like" (first segment has no dot) → stdlib.
//   - Otherwise → external import path.
//
// Phase 3 may relax the "workingDir must be the module root" constraint by
// walking up to find the nearest enclosing go.mod.
func (r *Resolver) Resolve(ctx context.Context, path, workingDir string) (ResolvedPath, error) {
	_ = ctx // reserved for future network-backed resolution
	if path == "" {
		return ResolvedPath{}, errors.New("empty path")
	}

	// Relative path: must start with "./" or be "." or start with "../" or be "..".
	if path == "." || path == ".." || strings.HasPrefix(path, "./") || strings.HasPrefix(path, "../") {
		if workingDir == "" {
			return ResolvedPath{}, errors.New("relative path requires working_dir")
		}
		if !filepath.IsAbs(workingDir) {
			return ResolvedPath{}, fmt.Errorf("working_dir must be absolute, got %q", workingDir)
		}
		modulePath, err := readModuleName(workingDir)
		if err != nil {
			return ResolvedPath{}, fmt.Errorf("read go.mod in %q: %w", workingDir, err)
		}
		cleanRel := filepath.ToSlash(filepath.Clean(path))
		if cleanRel == ".." || strings.HasPrefix(cleanRel, "../") {
			return ResolvedPath{}, fmt.Errorf("relative path %q escapes module root; working_dir must be the module root", path)
		}
		var importPath string
		if cleanRel == "." {
			importPath = modulePath
		} else {
			rel := strings.TrimPrefix(cleanRel, "./")
			importPath = modulePath + "/" + rel
		}
		return ResolvedPath{
			ImportPath: importPath,
			WorkingDir: workingDir,
			IsLocal:    true,
		}, nil
	}

	// Absolute filesystem path.
	if filepath.IsAbs(path) {
		modulePath, err := readModuleName(path)
		if err != nil {
			return ResolvedPath{}, fmt.Errorf("read go.mod in %q: %w", path, err)
		}
		if workingDir != "" && filepath.Clean(workingDir) != filepath.Clean(path) {
			return ResolvedPath{}, fmt.Errorf("working_dir %q does not match absolute path %q", workingDir, path)
		}
		return ResolvedPath{
			ImportPath: modulePath,
			WorkingDir: path,
			IsLocal:    true,
		}, nil
	}

	// Stdlib vs external heuristic: stdlib paths have no dot in the first
	// segment (e.g. "fmt", "encoding/json", "net/http").
	firstSeg := path
	if i := strings.Index(path, "/"); i >= 0 {
		firstSeg = path[:i]
	}
	if !strings.Contains(firstSeg, ".") {
		return ResolvedPath{
			ImportPath: path,
			IsStdlib:   true,
		}, nil
	}

	// External import path.
	return ResolvedPath{ImportPath: path}, nil
}

// readModuleName opens dir/go.mod and extracts the module path.
func readModuleName(dir string) (string, error) {
	f, err := os.Open(filepath.Join(dir, "go.mod"))
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		if !strings.HasPrefix(line, "module ") && !strings.HasPrefix(line, "module\t") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(line, "module"))
		if i := strings.Index(rest, "//"); i >= 0 {
			rest = strings.TrimSpace(rest[:i])
		}
		rest = strings.Trim(rest, `"`)
		if rest == "" {
			return "", errors.New("empty module declaration")
		}
		return rest, nil
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", errors.New("no module declaration found in go.mod")
}
