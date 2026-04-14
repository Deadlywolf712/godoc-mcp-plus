package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Sandbox owns per-import-path temporary Go modules used to resolve external
// packages without polluting the caller's working directory.
//
// Each sandbox lives under a single base directory created at startup and
// torn down in Close. Stdlib queries reuse a single shared project; external
// paths get their own temp project keyed by import path, so 'go get <path>'
// only runs once per path.
//
// Concurrency model: the mutex guards only map reads/writes. The slow work
// (go mod init + go get, up to ~150s) happens without the lock held. Parallel
// requests for different import paths proceed concurrently. Parallel requests
// for the same import path block on a per-key channel so we never double-fetch
// and never serialize unrelated paths behind a single slow go get.
type Sandbox struct {
	logger  *slog.Logger
	baseDir string

	mu           sync.Mutex
	closed       bool
	stdlibDir    string
	extDirs      map[string]string        // completed projects, keyed by import path
	pending      map[string]chan struct{} // in-flight inits, channel closed on completion
	goGetTimeout time.Duration
}

const stdlibKey = "\x00stdlib"

func NewSandbox(logger *slog.Logger) (*Sandbox, error) {
	base, err := os.MkdirTemp("", "godoc-mcp-plus-*")
	if err != nil {
		return nil, err
	}
	return &Sandbox{
		logger:       logger,
		baseDir:      base,
		extDirs:      make(map[string]string),
		pending:      make(map[string]chan struct{}),
		goGetTimeout: 120 * time.Second,
	}, nil
}

// GetOrCreateProject returns a working directory that can resolve importPath
// via 'go doc' / 'go list'. For stdlib paths, a shared project is used; for
// external paths, a per-path project is created on first use.
func (s *Sandbox) GetOrCreateProject(ctx context.Context, importPath string, isStdlib bool) (string, error) {
	key := importPath
	if isStdlib {
		key = stdlibKey
	}

	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return "", errors.New("sandbox closed")
		}

		// Fast path: already completed.
		if isStdlib && s.stdlibDir != "" {
			dir := s.stdlibDir
			s.mu.Unlock()
			return dir, nil
		}
		if !isStdlib {
			if dir, ok := s.extDirs[key]; ok {
				s.mu.Unlock()
				return dir, nil
			}
		}

		// In-flight path: wait on the existing init and retry.
		if ch, ok := s.pending[key]; ok {
			s.mu.Unlock()
			select {
			case <-ch:
				continue
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}

		// We are the first — register a pending channel and release the lock
		// before doing the slow work.
		ch := make(chan struct{})
		s.pending[key] = ch
		s.mu.Unlock()

		var (
			dir     string
			initErr error
		)
		if isStdlib {
			dir, initErr = s.initProject(ctx, "stdlib", "")
		} else {
			dir, initErr = s.initProject(ctx, sanitizeName(importPath), importPath)
		}

		s.mu.Lock()
		delete(s.pending, key)
		close(ch)
		if initErr != nil {
			s.mu.Unlock()
			return "", initErr
		}
		if s.closed {
			// Raced with Close: clean up the orphan we just created.
			s.mu.Unlock()
			_ = os.RemoveAll(dir)
			return "", errors.New("sandbox closed")
		}
		if isStdlib {
			s.stdlibDir = dir
		} else {
			s.extDirs[key] = dir
		}
		s.mu.Unlock()
		return dir, nil
	}
}

// initProject creates a temp module inside baseDir and optionally runs
// 'go get importPath' to populate the module graph.
func (s *Sandbox) initProject(ctx context.Context, name, importPath string) (string, error) {
	dir, err := os.MkdirTemp(s.baseDir, name+"-*")
	if err != nil {
		return "", fmt.Errorf("mkdir sandbox project: %w", err)
	}
	if err := runGo(ctx, dir, 30*time.Second, "mod", "init", "sandbox"); err != nil {
		return "", fmt.Errorf("go mod init: %w", err)
	}
	if importPath != "" {
		if err := runGo(ctx, dir, s.goGetTimeout, "get", importPath); err != nil {
			return "", fmt.Errorf("go get %s: %w", importPath, err)
		}
	}
	return dir, nil
}

// Close removes the entire sandbox base directory and marks the sandbox as
// closed so any subsequent GetOrCreateProject call fails fast.
func (s *Sandbox) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	if s.baseDir == "" {
		return
	}
	if err := os.RemoveAll(s.baseDir); err != nil {
		s.logger.Warn("sandbox cleanup failed", "dir", s.baseDir, "err", err)
	}
	s.baseDir = ""
	s.stdlibDir = ""
	s.extDirs = nil
}

func runGo(ctx context.Context, dir string, timeout time.Duration, args ...string) error {
	if dir == "" {
		return errors.New("runGo: empty dir")
	}
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, "go", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		if cmdCtx.Err() != nil {
			return fmt.Errorf("go %s timed out after %s: %w", args[0], timeout, cmdCtx.Err())
		}
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// sanitizeName strips characters from an import path to produce a filesystem-
// safe directory prefix.
func sanitizeName(path string) string {
	var b []byte
	for i := 0; i < len(path); i++ {
		c := path[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			b = append(b, c)
		case c == '-', c == '_':
			b = append(b, c)
		default:
			b = append(b, '_')
		}
	}
	if len(b) == 0 {
		return "pkg"
	}
	if len(b) > 40 {
		b = b[:40]
	}
	return string(b)
}
