// Package hclfile reads one HCL file into a struct and reports each problem with its position.
package hclfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// Diagnostic is one problem in a file. It renders as file:line:col: text. A problem with the
// whole file has line 0 and renders as file: text, and Cause holds the error under it.
type Diagnostic struct {
	File  string
	Line  int
	Col   int
	Text  string
	Cause error
}

// Error renders the position and the text.
func (d Diagnostic) Error() string {
	if d.Line == 0 {
		return fmt.Sprintf("%s: %s", d.File, d.Text)
	}

	return fmt.Sprintf("%s:%d:%d: %s", d.File, d.Line, d.Col, d.Text)
}

// Unwrap returns the error under the problem, if any.
func (d Diagnostic) Unwrap() error { return d.Cause }

var (
	quoted     = regexp.MustCompile(`"([^"]*)"`)
	suggestion = regexp.MustCompile(`Did you mean "([^"]*)"\?`)
)

// Decode reads the file at path and decodes it into target, a pointer to a struct with hcl tags.
// A key that the struct does not name is an error. The error holds one Diagnostic for each problem.
func Decode(path string, target any) error {
	src, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return unreadable(path, err)
	}

	body, err := parseSource(path, src)
	if err != nil {
		return err
	}

	return DecodeBody(body, target)
}

// Parse reads the file at path from fsys and returns its top-level body, so a caller can walk the
// blocks in file order. The error holds one Diagnostic for each syntax problem.
func Parse(fsys fs.FS, path string) (*hclsyntax.Body, error) {
	src, err := fs.ReadFile(fsys, path)
	if err != nil {
		return nil, unreadable(path, err)
	}

	return parseSource(path, src)
}

// DecodeBody decodes one body, such as the body of a block, into target with the rules of Decode.
func DecodeBody(body *hclsyntax.Body, target any) error {
	return report(gohcl.DecodeBody(body, nil, target), body)
}

func unreadable(path string, err error) Diagnostic {
	return Diagnostic{File: path, Text: "cannot read the file: " + err.Error() + ". Check that the file exists and that you can read it.", Cause: err}
}

func parseSource(path string, src []byte) (*hclsyntax.Body, error) {
	file, diags := hclsyntax.ParseConfig(src, path, hcl.InitialPos)

	body := syntaxBody(file)

	if diags.HasErrors() {
		return nil, report(diags, body)
	}

	return body, nil
}

func report(diags hcl.Diagnostics, body *hclsyntax.Body) error {
	var problems []error

	for _, diag := range diags {
		if diag.Severity == hcl.DiagError {
			problems = append(problems, describe(diag, body))
		}
	}

	return errors.Join(problems...)
}

// syntaxBody returns the body that the HCL syntax parser built for file. Any other body is empty.
func syntaxBody(file *hcl.File) *hclsyntax.Body {
	body, ok := file.Body.(*hclsyntax.Body)
	if !ok {
		return &hclsyntax.Body{}
	}

	return body
}

// At returns a Diagnostic at the start of r. A caller uses it for a problem that it finds after the decode.
func At(r hcl.Range, text string) Diagnostic {
	return Diagnostic{File: r.Filename, Line: r.Start.Line, Col: r.Start.Column, Text: text}
}

// describe turns one HCL diagnostic into a Diagnostic. The text is obee's own: HCL names the key
// and the nearest valid key, and the words around them are replaced.
func describe(diag *hcl.Diagnostic, body *hclsyntax.Body) Diagnostic {
	start := hcl.InitialPos
	file := ""

	if diag.Subject != nil {
		start, file = diag.Subject.Start, diag.Subject.Filename
	}

	return Diagnostic{File: file, Line: start.Line, Col: start.Column, Text: explain(diag, body, start)}
}

func explain(diag *hcl.Diagnostic, body *hclsyntax.Body, start hcl.Pos) string {
	name := firstQuoted(diag.Detail)

	switch {
	case strings.HasPrefix(diag.Summary, "Unsupported "):
		return unsupported(diag, name)
	case diag.Summary == "Attribute redefined":
		return fmt.Sprintf("`%s` is set twice. Keep one.", name)
	case strings.HasPrefix(diag.Summary, "Duplicate "):
		return fmt.Sprintf("block `%s` appears twice. Keep one.", strings.Fields(diag.Summary)[1])
	case diag.Summary == "Missing required argument":
		return fmt.Sprintf("`%s` is required. Add it.", name)
	case diag.Summary == "Unsuitable value type":
		return wrongType(attributeAt(body, start), plainType(diag.Detail))
	default:
		return strings.TrimSuffix(diag.Detail, ".") + ". Fix the syntax of the file."
	}
}

// unsupported words a key or block that the struct does not name.
func unsupported(diag *hcl.Diagnostic, name string) string {
	if diag.Summary == "Unsupported argument" {
		return unknown(diag.Detail, name, "key", "`%s` is a block, not a key. Write `%[1]s { ... }`.", "Did you mean to define a block")
	}

	return unknown(diag.Detail, name, "block", "`%s` is a key, not a block. Write `%[1]s = value`.", "Did you mean to define argument")
}

// unknown words a key or block that the struct does not name. kind is the word for what the user wrote.
// swapped is the message when the user wrote a key for a block or a block for a key, and marker is the HCL
// text that shows this case.
func unknown(detail, name, kind, swapped, marker string) string {
	if strings.Contains(detail, marker) {
		return fmt.Sprintf(swapped, name)
	}

	if match := suggestion.FindStringSubmatch(detail); match != nil {
		return fmt.Sprintf("unknown %s `%s`. Remove it, or use `%s`.", kind, name, match[1])
	}

	return fmt.Sprintf("unknown %s `%s`. Remove it.", kind, name)
}

func wrongType(key, want string) string {
	if want == "" {
		return fmt.Sprintf("`%s` has the wrong type. Fix the value.", key)
	}

	return fmt.Sprintf("`%s` has the wrong type. Use %s.", key, want)
}

// plainType names the type that HCL expects in plain words, or returns "" for a type it does not know.
func plainType(detail string) string {
	detail, _, _ = strings.Cut(detail, "required")

	switch {
	case strings.Contains(detail, "list"), strings.Contains(detail, "set"), strings.Contains(detail, "tuple"):
		return "a list"
	case strings.Contains(detail, "number"):
		return "a number"
	case strings.Contains(detail, "bool"):
		return "true or false"
	case strings.Contains(detail, "string"):
		return "text"
	default:
		return ""
	}
}

func firstQuoted(text string) string {
	if match := quoted.FindStringSubmatch(text); match != nil {
		return match[1]
	}

	return ""
}

// attributeAt returns the name of the attribute, in body or in a block below it, whose source holds pos.
func attributeAt(body *hclsyntax.Body, pos hcl.Pos) string {
	for name, attr := range body.Attributes {
		if attr.SrcRange.ContainsPos(pos) {
			return name
		}
	}

	for _, block := range body.Blocks {
		if name := attributeAt(block.Body, pos); name != "" {
			return name
		}
	}

	return ""
}
