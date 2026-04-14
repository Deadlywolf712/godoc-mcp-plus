package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// Renderer runs the Go toolchain to produce documentation text and
// sub-package listings.
type Renderer struct {
	logger  *slog.Logger
	sandbox *Sandbox
	cache   *DocCache
	timeout time.Duration
}

func NewRenderer(logger *slog.Logger, sandbox *Sandbox, cache *DocCache) *Renderer {
	return &Renderer{
		logger:  logger,
		sandbox: sandbox,
		cache:   cache,
		timeout: 30 * time.Second,
	}
}

// RenderText returns the output of 'go doc <flags> <importPath> [target]'.
// The flags slice is sorted when building the cache key so that semantically
// identical calls with different flag orderings share a single cache entry.
func (r *Renderer) RenderText(ctx context.Context, resolved ResolvedPath, target string, flags []string) (string, error) {
	dir, err := r.workingDirFor(ctx, resolved)
	if err != nil {
		return "", err
	}

	sortedFlags := append([]string(nil), flags...)
	sort.Strings(sortedFlags)

	args := make([]string, 0, len(sortedFlags)+3)
	args = append(args, "doc")
	args = append(args, sortedFlags...)
	args = append(args, resolved.ImportPath)
	if target != "" {
		args = append(args, target)
	}

	key := dir + "|" + strings.Join(args, " ")
	if cached, ok := r.cache.Get(key); ok {
		return cached, nil
	}

	cmdCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "go", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		if cmdCtx.Err() != nil {
			return "", fmt.Errorf("go doc timed out after %s: %w", r.timeout, cmdCtx.Err())
		}
		r.logger.Debug("go doc failed", "args", args, "dir", dir, "err", err, "output", string(output))
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	out := string(output)
	r.cache.Put(key, out)
	return out, nil
}

// ListPackages runs 'go list <importPath>/...' and returns the matching
// import paths, filtering out internal/ packages unless includeInternal is set.
// Returns an empty slice (never nil) on no matches so JSON encoding produces
// "packages": [] instead of "packages": null.
func (r *Renderer) ListPackages(ctx context.Context, resolved ResolvedPath, includeInternal bool) ([]string, error) {
	dir, err := r.workingDirFor(ctx, resolved)
	if err != nil {
		return nil, err
	}

	pattern := resolved.ImportPath + "/..."

	cmdCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "go", "list", "-f", "{{.ImportPath}}", pattern)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	out := []string{}
	if err != nil {
		if cmdCtx.Err() != nil {
			return nil, fmt.Errorf("go list timed out after %s: %w", r.timeout, cmdCtx.Err())
		}
		if strings.Contains(string(output), "matched no packages") {
			return out, nil
		}
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}

	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		p := strings.TrimSpace(line)
		if p == "" {
			continue
		}
		if !includeInternal && containsInternal(p) {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// workingDirFor returns the directory to run go tools from for this resolved
// path. Local paths use the caller's working_dir directly; stdlib and external
// paths use a sandbox directory.
func (r *Renderer) workingDirFor(ctx context.Context, resolved ResolvedPath) (string, error) {
	if resolved.IsLocal {
		if resolved.WorkingDir == "" {
			return "", errors.New("local path with empty working_dir")
		}
		return resolved.WorkingDir, nil
	}
	return r.sandbox.GetOrCreateProject(ctx, resolved.ImportPath, resolved.IsStdlib)
}

func containsInternal(importPath string) bool {
	for _, seg := range strings.Split(importPath, "/") {
		if seg == "internal" {
			return true
		}
	}
	return false
}
