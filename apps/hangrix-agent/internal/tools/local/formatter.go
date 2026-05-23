package local

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// Formatter is the minimal interface for a language-specific code formatter.
// Adding support for a new language means implementing this interface and
// registering it with FormatterRegistry — no changes to the central dispatch
// logic are required.
type Formatter interface {
	// Name returns the canonical formatter name exposed in the edit result
	// (e.g. "gofmt", "prettier", "rustfmt").
	Name() string
	// Supports reports whether this formatter can handle the file at path
	// (typically decided by extension).
	Supports(path string) bool
	// Format runs the formatter on content for the file at path.  Returns
	// the formatted bytes or an error (missing binary / non-zero exit).
	Format(path string, content []byte) ([]byte, error)
}

// FormatterRegistry holds the ordered list of registered formatters.
// The first formatter whose Supports returns true for a given path wins.
type FormatterRegistry struct {
	formatters []Formatter
}

// NewFormatterRegistry creates a registry with the three default formatters
// registered (gofmt, prettier, rustfmt). Callers can append more via
// Register before the agent starts handling requests.
func NewFormatterRegistry() *FormatterRegistry {
	r := &FormatterRegistry{}
	r.Register(&GofmtFormatter{})
	r.Register(&PrettierFormatter{})
	r.Register(&RustfmtFormatter{})
	return r
}

// Register appends a formatter. Registration order matters: earlier
// formatters are tested first by Find.
func (r *FormatterRegistry) Register(f Formatter) {
	r.formatters = append(r.formatters, f)
}

// Find returns the first registered formatter that Supports path, or nil.
func (r *FormatterRegistry) Find(path string) Formatter {
	for _, f := range r.formatters {
		if f.Supports(path) {
			return f
		}
	}
	return nil
}

// ----- gofmt ---------------------------------------------------------------

type GofmtFormatter struct{}

func (f *GofmtFormatter) Name() string { return "gofmt" }

func (f *GofmtFormatter) Supports(path string) bool {
	return strings.ToLower(filepath.Ext(path)) == ".go"
}

func (f *GofmtFormatter) Format(_ string, content []byte) ([]byte, error) {
	cmd := exec.Command("gofmt")
	cmd.Stdin = bytes.NewReader(content)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("gofmt: %w", err)
	}
	return out.Bytes(), nil
}

// ----- prettier -------------------------------------------------------------

type PrettierFormatter struct{}

func (f *PrettierFormatter) Name() string { return "prettier" }

func (f *PrettierFormatter) Supports(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".js", ".ts", ".jsx", ".tsx", ".mjs", ".cjs", ".vue", ".json", ".md", ".yaml", ".yml":
		return true
	}
	return false
}

func (f *PrettierFormatter) Format(path string, content []byte) ([]byte, error) {
	cmd := exec.Command("prettier", "--stdin-filepath", path)
	cmd.Stdin = bytes.NewReader(content)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("prettier: %w", err)
	}
	return out.Bytes(), nil
}

// ----- rustfmt --------------------------------------------------------------

type RustfmtFormatter struct{}

func (f *RustfmtFormatter) Name() string { return "rustfmt" }

func (f *RustfmtFormatter) Supports(path string) bool {
	return strings.ToLower(filepath.Ext(path)) == ".rs"
}

func (f *RustfmtFormatter) Format(_ string, content []byte) ([]byte, error) {
	cmd := exec.Command("rustfmt", "--emit=stdout")
	cmd.Stdin = bytes.NewReader(content)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("rustfmt: %w", err)
	}
	return out.Bytes(), nil
}
