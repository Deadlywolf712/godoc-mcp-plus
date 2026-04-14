package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const getDocDescription = `Return Go documentation for a package or a specific symbol within it.

ALWAYS prefer this tool over reading package source files when you want to know
what a Go API does. It handles stdlib, third-party modules, and local packages.

Examples:
  - path="fmt" — stdlib package
  - path="github.com/foo/bar" — external module (fetched into a temp sandbox
    if not already present)
  - path="." + working_dir="/abs/path/to/module" — local package
  - path="fmt" + target="Printf" — specific function
  - path="io" + target="Reader.Read" — method on a type
  - cmd_flags=["-all"] — include unexported symbols
  - cmd_flags=["-src"] — include source code

The response includes paginated text output and structured metadata. If the
output is long, use the page and page_size fields to scroll.`

const listPackagesDescription = `List sub-packages under a Go package path.

Returns import paths for every package under the given root. Handles stdlib,
external modules, and local modules the same way as get_doc. By default,
'internal/' packages are excluded — set include_internal=true to include them.`

// GetDocIn is the input schema for the get_doc tool.
type GetDocIn struct {
	Path       string   `json:"path" jsonschema:"Import path (e.g. 'fmt' or 'github.com/foo/bar') or '.' for the current module"`
	Target     string   `json:"target,omitempty" jsonschema:"Optional symbol within the package, e.g. 'Printf' or 'Reader.Read'"`
	WorkingDir string   `json:"working_dir,omitempty" jsonschema:"Absolute working directory; required when path starts with '.'"`
	CmdFlags   []string `json:"cmd_flags,omitempty" jsonschema:"Optional flags to pass through to 'go doc'; allowed: -all, -src, -u, -short, -c"`
	Page       int      `json:"page,omitempty" jsonschema:"1-based page number for pagination (default 1)"`
	PageSize   int      `json:"page_size,omitempty" jsonschema:"Lines per page (default 1000, clamped 100..5000)"`
}

// GetDocOut is the structured output of the get_doc tool.
type GetDocOut struct {
	Path       string         `json:"path" jsonschema:"Resolved import path"`
	Target     string         `json:"target,omitempty" jsonschema:"Resolved symbol, if requested"`
	Text       string         `json:"text" jsonschema:"Formatted go doc output (paginated)"`
	Page       int            `json:"page" jsonschema:"Current page number"`
	TotalPages int            `json:"total_pages" jsonschema:"Total pages at this page_size"`
	TotalLines int            `json:"total_lines" jsonschema:"Total output lines"`
	Structured *StructuredDoc `json:"structured,omitempty" jsonschema:"Structured metadata (may be nil if the structured loader failed)"`
}

// StructuredDoc is populated by the native go/packages loader in Phase 3.
// Phase 2 always returns nil for Structured; the field exists now so the
// output schema is stable from day one.
type StructuredDoc struct {
	PackageName string   `json:"package_name"`
	ImportPath  string   `json:"import_path"`
	GoFiles     []string `json:"go_files,omitempty"`
}

// ListPackagesIn is the input schema for list_packages.
type ListPackagesIn struct {
	Path            string `json:"path" jsonschema:"Root import path"`
	WorkingDir      string `json:"working_dir,omitempty" jsonschema:"Absolute working directory; required when path starts with '.'"`
	IncludeInternal bool   `json:"include_internal,omitempty" jsonschema:"Include internal/ packages (default false)"`
}

// ListPackagesOut is the structured output of list_packages.
type ListPackagesOut struct {
	Root     string   `json:"root" jsonschema:"Resolved root import path"`
	Packages []string `json:"packages" jsonschema:"Sub-package import paths"`
}

// deps is the process-wide dependency bundle wired from main.
type deps struct {
	logger   *slog.Logger
	resolver *Resolver
	renderer *Renderer
	sandbox  *Sandbox
}

func newDeps(logger *slog.Logger) (*deps, error) {
	sandbox, err := NewSandbox(logger)
	if err != nil {
		return nil, fmt.Errorf("sandbox init: %w", err)
	}
	cache := NewDocCache(500, 5*time.Minute)
	return &deps{
		logger:   logger,
		resolver: NewResolver(logger),
		renderer: NewRenderer(logger, sandbox, cache),
		sandbox:  sandbox,
	}, nil
}

func (d *deps) Close() {
	if d.sandbox != nil {
		d.sandbox.Close()
	}
}

// GetDoc handles the get_doc tool.
func (d *deps) GetDoc(ctx context.Context, req *mcp.CallToolRequest, in GetDocIn) (*mcp.CallToolResult, GetDocOut, error) {
	if in.Path == "" {
		return nil, GetDocOut{}, errors.New("path is required")
	}
	if err := validateFlags(in.CmdFlags); err != nil {
		return nil, GetDocOut{}, err
	}

	resolved, err := d.resolver.Resolve(ctx, in.Path, in.WorkingDir)
	if err != nil {
		return nil, GetDocOut{}, fmt.Errorf("resolve %q: %w", in.Path, err)
	}

	text, err := d.renderer.RenderText(ctx, resolved, in.Target, in.CmdFlags)
	if err != nil {
		return nil, GetDocOut{}, formatGoDocError(err, resolved.ImportPath, in.Target)
	}

	pageText, actualPage, totalPages, totalLines := paginate(text, clampPage(in.Page), clampPageSize(in.PageSize))

	return nil, GetDocOut{
		Path:       resolved.ImportPath,
		Target:     in.Target,
		Text:       pageText,
		Page:       actualPage,
		TotalPages: totalPages,
		TotalLines: totalLines,
		Structured: nil, // Phase 3: hybrid go/packages load
	}, nil
}

// ListPackages handles the list_packages tool.
func (d *deps) ListPackages(ctx context.Context, req *mcp.CallToolRequest, in ListPackagesIn) (*mcp.CallToolResult, ListPackagesOut, error) {
	if in.Path == "" {
		return nil, ListPackagesOut{}, errors.New("path is required")
	}
	resolved, err := d.resolver.Resolve(ctx, in.Path, in.WorkingDir)
	if err != nil {
		return nil, ListPackagesOut{}, fmt.Errorf("resolve %q: %w", in.Path, err)
	}
	pkgs, err := d.renderer.ListPackages(ctx, resolved, in.IncludeInternal)
	if err != nil {
		return nil, ListPackagesOut{}, fmt.Errorf("list sub-packages of %q: %w", resolved.ImportPath, err)
	}
	return nil, ListPackagesOut{Root: resolved.ImportPath, Packages: pkgs}, nil
}

// --- helpers ---

var allowedGoDocFlags = map[string]struct{}{
	"-all":   {},
	"-src":   {},
	"-u":     {},
	"-short": {},
	"-c":     {},
}

func validateFlags(flags []string) error {
	for _, f := range flags {
		if _, ok := allowedGoDocFlags[f]; !ok {
			allowed := make([]string, 0, len(allowedGoDocFlags))
			for k := range allowedGoDocFlags {
				allowed = append(allowed, k)
			}
			sort.Strings(allowed)
			return fmt.Errorf("disallowed flag %q; allowed: %s", f, strings.Join(allowed, ", "))
		}
	}
	return nil
}

func clampPage(p int) int {
	if p < 1 {
		return 1
	}
	return p
}

func clampPageSize(s int) int {
	switch {
	case s <= 0:
		return 1000
	case s < 100:
		return 100
	case s > 5000:
		return 5000
	default:
		return s
	}
}

// paginate slices text into 1-based pages of pageSize lines.
// Returns the page text (with a metadata header), the page number actually
// used after clamping, total pages, and total line count across all pages.
func paginate(text string, page, pageSize int) (string, int, int, int) {
	lines := strings.Split(text, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	total := len(lines)
	if total == 0 {
		return "", 1, 1, 0
	}
	totalPages := (total + pageSize - 1) / pageSize
	if page > totalPages {
		page = totalPages
	}
	start := (page - 1) * pageSize
	end := start + pageSize
	if end > total {
		end = total
	}
	header := fmt.Sprintf("Page %d of %d (lines %d-%d of %d)\n\n", page, totalPages, start+1, end, total)
	return header + strings.Join(lines[start:end], "\n"), page, totalPages, total
}

// formatGoDocError turns a raw go doc / go list failure into an actionable
// hint for the LLM, mapping known stderr patterns to numbered remediation.
func formatGoDocError(err error, path, target string) error {
	msg := err.Error()
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "no such package"),
		strings.Contains(lower, "cannot find package"),
		strings.Contains(lower, "is not in std"),
		strings.Contains(lower, "cannot find module"):
		return fmt.Errorf(`package %q not found.
1. Verify the import path is spelled correctly (Go paths are case-sensitive).
2. For external packages, ensure network access to fetch into the sandbox.
3. For local packages, pass working_dir pointing at the module root and use path="." or a relative path.
underlying error: %w`, path, err)
	case strings.Contains(lower, "no symbol"):
		return fmt.Errorf(`symbol %q not found in package %q.
1. Check the symbol name for typos; Go identifiers are case-sensitive.
2. For unexported symbols, add cmd_flags=["-all"] to include them.
3. For methods, use the form "TypeName.MethodName".
underlying error: %w`, target, path, err)
	case strings.Contains(lower, "build constraints exclude all go files"):
		return fmt.Errorf(`package %q exists but all its Go files are excluded by build constraints (e.g. this GOOS/GOARCH).
1. The package may be platform-specific.
2. Try setting GOOS/GOARCH in the caller environment if needed.
underlying error: %w`, path, err)
	case strings.Contains(lower, "no go files in"):
		return fmt.Errorf(`directory for %q contains no Go files directly.
1. The path may be a parent of several packages rather than a package itself.
2. Call list_packages with the same path to discover sub-packages, then call get_doc on one of those.
underlying error: %w`, path, err)
	default:
		return fmt.Errorf("get_doc failed for %q: %w", path, err)
	}
}
