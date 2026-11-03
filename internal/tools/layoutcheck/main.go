// Command layoutcheck enforces the Go source-layout conventions that no
// golangci-lint linter covers (see .claude/CLAUDE.md, "Golang Best Practices"):
//
//   - Methods grouped by receiver: within a non-test file, a type's methods
//     form one unbroken run, with no other declaration between its first and
//     last method.
//   - No compile-time interface guards: no top-level `var _ I = (*T)(nil)`.
//
// It walks the given directories (default "."), skipping hidden, vendor,
// node_modules and testdata directories and generated files, prints each
// finding as "file:line: message", and exits 1 when there is any. It is run by
// `task lint`.
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// finding is one convention break at a source position.
type finding struct {
	pos token.Position
	msg string
}

// String renders the finding as "file:line: message".
func (f finding) String() string {
	return fmt.Sprintf("%s:%d: %s", f.pos.Filename, f.pos.Line, f.msg)
}

// run checks every Go file under roots and reports the findings on out. It
// returns the process exit code: 0 when clean, 1 on findings, 2 on an error.
func run(roots []string, out, errOut io.Writer) int {
	if len(roots) == 0 {
		roots = []string{"."}
	}
	fset := token.NewFileSet()
	found := 0
	for _, root := range roots {
		n, err := walk(fset, root, out)
		if err != nil {
			_, _ = fmt.Fprintf(errOut, "layoutcheck: %v\n", err)
			return 2
		}
		found += n
	}
	if found > 0 {
		return 1
	}

	return 0
}

// walk checks every Go file under root, prints its findings on out and
// returns how many there were.
func walk(fset *token.FileSet, root string, out io.Writer) (int, error) {
	found := 0
	// root is a directory the developer passed on the command line.
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error { //nolint:gosec // see above
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		findings, err := checkFile(fset, path)
		if err != nil {
			return err
		}
		for _, f := range findings {
			_, _ = fmt.Fprintln(out, f)
		}
		found += len(findings)

		return nil
	})
	if err != nil {
		return found, fmt.Errorf("walk %s: %w", root, err)
	}

	return found, nil
}

// skipDir reports whether a directory named name is not walked.
func skipDir(name string) bool {
	return strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules" || name == "testdata"
}

// checkFile parses the file at path and returns its findings. Generated files
// have none.
func checkFile(fset *token.FileSet, path string) ([]finding, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	return checkSource(fset, path, src)
}

// checkSource returns the findings of one file's source. The method-grouping
// rule applies to non-test files only, matching the funcorder exclusion for
// tests in .golangci.yml.
func checkSource(fset *token.FileSet, path string, src []byte) ([]finding, error) {
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if ast.IsGenerated(file) {
		return nil, nil
	}
	found := interfaceGuards(fset, file)
	if !strings.HasSuffix(path, "_test.go") {
		found = append(found, splitMethodRuns(fset, file)...)
	}

	return found, nil
}

// interfaceGuards finds each top-level `var _ T = ...` declaration.
func interfaceGuards(fset *token.FileSet, file *ast.File) []finding {
	var found []finding
	for _, d := range file.Decls {
		gen, ok := d.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || vs.Type == nil || len(vs.Values) == 0 {
				continue
			}
			for _, name := range vs.Names {
				if name.Name == "_" {
					found = append(found, finding{
						pos: fset.Position(name.Pos()),
						msg: "compile-time interface guard: the consumer's use already checks the type",
					})
				}
			}
		}
	}

	return found
}

// splitMethodRuns finds every declaration that sits between a type's first
// and last method in the file.
func splitMethodRuns(fset *token.FileSet, file *ast.File) []finding {
	first, last := map[string]int{}, map[string]int{}
	var order []string
	for i, d := range file.Decls {
		recv := receiverOf(d)
		if recv == "" {
			continue
		}
		if _, ok := first[recv]; !ok {
			first[recv] = i
			order = append(order, recv)
		}
		last[recv] = i
	}

	var found []finding
	for _, recv := range order {
		for i := first[recv] + 1; i < last[recv]; i++ {
			d := file.Decls[i]
			if receiverOf(d) == recv {
				continue
			}
			found = append(found, finding{
				pos: fset.Position(d.Pos()),
				msg: fmt.Sprintf("%s splits the methods of %s; keep a type's methods in one run", describe(d), recv),
			})
		}
	}

	return found
}

// receiverOf returns the receiver type name of a method declaration, or "".
func receiverOf(d ast.Decl) string {
	fn, ok := d.(*ast.FuncDecl)
	if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}

	return typeName(fn.Recv.List[0].Type)
}

// typeName strips pointers and type arguments from a receiver type.
func typeName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return typeName(t.X)
	case *ast.IndexExpr:
		return typeName(t.X)
	case *ast.IndexListExpr:
		return typeName(t.X)
	case *ast.Ident:
		return t.Name
	default:
		return ""
	}
}

// describe names a declaration for a finding: "func f", "method T.m",
// "type T", "const block" or "var block".
func describe(d ast.Decl) string {
	switch d := d.(type) {
	case *ast.FuncDecl:
		if recv := receiverOf(d); recv != "" {
			return "method " + recv + "." + d.Name.Name
		}
		return "func " + d.Name.Name
	case *ast.GenDecl:
		if d.Tok == token.TYPE && len(d.Specs) > 0 {
			if ts, ok := d.Specs[0].(*ast.TypeSpec); ok {
				return "type " + ts.Name.Name
			}
		}

		return d.Tok.String() + " block"
	default:
		return "declaration"
	}
}
