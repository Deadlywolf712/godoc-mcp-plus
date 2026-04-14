# godoc-mcp-plus

A polished Go MCP server that exposes Go package documentation to LLMs. Built on the official [`github.com/modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk).

Source: https://github.com/Deadlywolf712/godoc-mcp-plus

## What it is

`godoc-mcp-plus` is an MCP server that wraps `go doc` and `go list` and serves them as two tools (`get_doc`, `list_packages`) to any MCP-capable client. It handles the standard library, external modules, and local packages through a single interface, with pagination, caching, error remediation, and a singleflight-guarded sandbox for external fetches.

## Why use it

- LLM coding assistants waste tokens reading entire `.go` files when all they need is the public API of a package. `get_doc` returns exactly that, with the same fidelity as `go doc`.
- External modules are fetched on demand into a sandbox, so the model can read docs for packages that are not vendored in the current project.
- Long documentation is paginated with a line-range header, so the model can scroll without blowing its context window.
- Common failure modes (symbol not found, package not found, build constraints, `go get` failures) are rewritten into numbered remediation steps the model can act on.

## Install

```sh
go install github.com/Deadlywolf712/godoc-mcp-plus@latest
```

The binary lands in `$GOPATH/bin` (or `%USERPROFILE%\go\bin` on Windows). Requires Go 1.26.2 or newer to build.

## Register with Claude

### Claude Code

```sh
claude mcp add --scope user godoc-plus <path-to-binary>
```

Claude Code sometimes spawns MCP servers with a stale parent `PATH` that lacks `go` or `git`. The server auto-discovers common install locations at startup and prepends the right bin directory, so in most cases this just works. If it does not, set `env.PATH` explicitly in `~/.claude.json`:

```json
{
  "mcpServers": {
    "godoc-plus": {
      "command": "C:\\Users\\you\\go\\bin\\godoc-mcp-plus.exe",
      "env": {
        "PATH": "C:\\Program Files\\Go\\bin;C:\\Program Files\\Git\\cmd;C:\\Windows\\System32;C:\\Windows"
      }
    }
  }
}
```

### Claude Desktop

Config location:

- Windows: `%APPDATA%\Claude\claude_desktop_config.json`
- macOS: `~/Library/Application Support/Claude/claude_desktop_config.json`

```json
{
  "mcpServers": {
    "godoc-plus": {
      "command": "C:\\Users\\you\\go\\bin\\godoc-mcp-plus.exe",
      "env": {
        "PATH": "C:\\Program Files\\Go\\bin;C:\\Program Files\\Git\\cmd;C:\\Windows\\System32;C:\\Windows"
      }
    }
  }
}
```

After editing, fully quit Claude Desktop from the system tray icon and relaunch. Closing the window alone leaves the previous process running and your config change will not take effect.

## Usage examples

All tool calls return a structured object. `get_doc` returns `GetDocOut`; `list_packages` returns `ListPackagesOut`.

Stdlib, simple symbol:

```json
{
  "tool": "get_doc",
  "arguments": {
    "path": "fmt",
    "target": "Printf"
  }
}
```

Method on a type:

```json
{
  "tool": "get_doc",
  "arguments": {
    "path": "io",
    "target": "Reader.Read"
  }
}
```

Whole package with unexported symbols:

```json
{
  "tool": "get_doc",
  "arguments": {
    "path": "net/http",
    "cmd_flags": ["-all"],
    "page": 1,
    "page_size": 1000
  }
}
```

External module (fetched into the sandbox on first use):

```json
{
  "tool": "get_doc",
  "arguments": {
    "path": "github.com/spf13/cobra",
    "target": "Command"
  }
}
```

Local package with a working directory:

```json
{
  "tool": "get_doc",
  "arguments": {
    "path": ".",
    "working_dir": "C:\\Users\\you\\code\\mymod"
  }
}
```

List sub-packages:

```json
{
  "tool": "list_packages",
  "arguments": {
    "path": "golang.org/x/tools",
    "include_internal": false
  }
}
```

A successful `get_doc` response looks like:

```json
{
  "path": "fmt",
  "target": "Printf",
  "text": "Page 1 of 1 (lines 1-6 of 6)\n\nfunc Printf(format string, a ...any) (n int, err error)\n    Printf formats according to a format specifier...",
  "page": 1,
  "total_pages": 1,
  "total_lines": 6,
  "structured": null
}
```

`structured` is reserved for a future `go/packages`-backed metadata loader and is always `null` today.

## Flags

```
-http addr       if set, serve streamable HTTP at this address instead of stdio
-log-level lvl   slog level: debug, info, warn, error (default "info")
```

Default transport is stdio. Use `-http :8080` to serve the streamable HTTP transport instead.

## Architecture

The server takes a hybrid shell-out approach: `get_doc` invokes `go doc` and `list_packages` invokes `go list` as subprocesses. This preserves the exact text output that Go developers already know from the CLI. A native `go/packages` structured metadata path is planned for Phase 3+; the `Structured` output field is already defined so the schema is stable.

External module fetches go through a singleflight-guarded sandbox, so a slow `go get` for one module does not block unrelated requests for packages that are already cached. A two-tier cache sits in front of the renderer: the sandbox caches fetched module trees, and a TTL + size-capped doc cache (5 minutes, 500 entries) memoizes rendered text output.

Other hardening:

- Flag allowlist: only `-all`, `-src`, `-u`, `-short`, `-c` are passed through to `go doc`. Anything else is rejected before the subprocess is spawned.
- Auto-discovery of `go` and `git` on startup, with common install locations tried when `exec.LookPath` fails.
- `ReadOnlyHint` and `IdempotentHint` tool annotations so MCP hosts can auto-approve calls.
- `ServerOptions.Instructions` set so the model knows to prefer this tool over reading source files.
- slog structured logging to stderr.

## Known limitations / roadmap

Planned work, not bugs:

- No native structured metadata yet. The `structured` field in `GetDocOut` is always `null`; Phase 3+ will populate it via `go/packages`.
- No `go.work` workspace awareness. Local resolution assumes a single module rooted at `working_dir`.
- No version-pinned doc lookup. You get whatever version the sandbox resolves; there is no `@v1.2.3` suffix support.
- No TTL-based eviction of sandbox temp projects. Cleanup happens on process shutdown; long-lived servers will grow their sandbox over time.

## Inspired by

Inspired by [mrjoshuak/godoc-mcp](https://github.com/mrjoshuak/godoc-mcp), which uses `mark3labs/mcp-go`. This project uses the official `modelcontextprotocol/go-sdk` and adds pagination, remediation-style errors, a singleflight-guarded sandbox, a flag allowlist, and tool-on-PATH auto-discovery.

## License

MIT. See [LICENSE](LICENSE).
