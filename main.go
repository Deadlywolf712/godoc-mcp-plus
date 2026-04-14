// Command godoc-mcp-plus exposes Go package documentation over MCP.
//
// Two tools:
//   - get_doc: return documentation for a package or symbol
//   - list_packages: enumerate sub-packages under a root import path
//
// Transport defaults to stdio. Pass -http <addr> for streamable HTTP.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const serverInstructions = `godoc-mcp-plus exposes high-fidelity Go documentation for any package on the host: standard library, external modules, and local projects.

Prefer get_doc before reading Go source files: it returns the same output as 'go doc' with pagination, a flag allowlist, and (in future versions) structured metadata. Use list_packages to discover sub-packages under a root import path.`

func main() {
	httpAddr := flag.String("http", "", "if set, serve streamable HTTP at this address instead of stdio")
	logLevel := flag.String("log-level", "info", "slog level: debug, info, warn, error")
	flag.Parse()

	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(*logLevel)); err != nil {
		log.Fatalf("invalid -log-level %q: %v", *logLevel, err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))

	// 'go get' calls 'git' internally for external module resolution, so we
	// need both on PATH. MCP hosts sometimes spawn us with a stale parent env
	// that lacks one or both; auto-discover as a best-effort fallback.
	ensureToolOnPath(logger, "go", goCandidates())
	ensureToolOnPath(logger, "git", gitCandidates())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	d, err := newDeps(logger)
	if err != nil {
		log.Fatalf("startup: %v", err)
	}
	defer d.Close()

	server := mcp.NewServer(
		&mcp.Implementation{Name: "godoc-mcp-plus", Version: "0.1.0"},
		&mcp.ServerOptions{
			Logger:       logger,
			Instructions: serverInstructions,
		},
	)

	readOnlyIdempotent := &mcp.ToolAnnotations{
		ReadOnlyHint:   true,
		IdempotentHint: true,
	}

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_doc",
		Description: getDocDescription,
		Annotations: readOnlyIdempotent,
	}, d.GetDoc)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_packages",
		Description: listPackagesDescription,
		Annotations: readOnlyIdempotent,
	}, d.ListPackages)

	if *httpAddr != "" {
		handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
			return server
		}, nil)
		srv := &http.Server{
			Addr:              *httpAddr,
			Handler:           handler,
			ReadHeaderTimeout: 10 * time.Second,
		}
		go func() {
			<-ctx.Done()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdownCtx)
		}()
		logger.Info("streamable http starting", "addr", *httpAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http server failed: %v", err)
		}
		return
	}

	logger.Info("stdio server starting")
	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil {
		logger.Error("server run failed", "err", err)
		os.Exit(1)
	}
}

// ensureToolOnPath guarantees the given tool is reachable by child processes
// spawned later. If already on PATH it does nothing; otherwise it tries common
// install locations and, if one works, prepends that bin directory to the
// process PATH. Makes the server robust to MCP hosts that inherited a stale
// parent env lacking the Go toolchain or git.
func ensureToolOnPath(logger *slog.Logger, name string, candidates []string) {
	if _, err := exec.LookPath(name); err == nil {
		return
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			binDir := filepath.Dir(c)
			oldPath := os.Getenv("PATH")
			sep := string(os.PathListSeparator)
			if err := os.Setenv("PATH", binDir+sep+oldPath); err != nil {
				logger.Warn("auto-discover: setenv PATH failed", "tool", name, "err", err)
				return
			}
			logger.Info("tool auto-discovered", "tool", name, "path", c, "prepended_to_PATH", binDir)
			return
		}
	}
	logger.Warn("tool not found on PATH and auto-discovery failed; calls that need it will fail",
		"tool", name, "tried", candidates)
}

func goCandidates() []string {
	if runtime.GOOS == "windows" {
		return []string{
			`C:\Program Files\Go\bin\go.exe`,
			`C:\Go\bin\go.exe`,
			filepath.Join(os.Getenv("USERPROFILE"), "go", "bin", "go.exe"),
		}
	}
	home, _ := os.UserHomeDir()
	return []string{
		"/usr/local/go/bin/go",
		"/opt/go/bin/go",
		filepath.Join(home, "go", "bin", "go"),
		"/usr/bin/go",
	}
}

func gitCandidates() []string {
	if runtime.GOOS == "windows" {
		return []string{
			`C:\Program Files\Git\cmd\git.exe`,
			`C:\Program Files\Git\bin\git.exe`,
			`C:\Program Files (x86)\Git\cmd\git.exe`,
			`C:\Program Files (x86)\Git\bin\git.exe`,
		}
	}
	return []string{
		"/usr/bin/git",
		"/usr/local/bin/git",
		"/opt/homebrew/bin/git",
	}
}
