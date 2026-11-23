// Package ifacetest lists the methods of interfaces from their source, so a suite test can fail when an interface gains a method that the suite does not run.
package ifacetest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"slices"
)

// Methods returns the sorted method names of the named interfaces, as the files declare them. An embedded interface of the same files counts.
func Methods(files []string, names ...string) ([]string, error) {
	specs, err := interfaces(files)
	if err != nil {
		return nil, err
	}

	found := map[string]bool{}

	for _, name := range names {
		if err := add(specs, name, found); err != nil {
			return nil, err
		}
	}

	return slices.Sorted(maps.Keys(found)), nil
}

// interfaces parses the files and returns each interface type by name.
func interfaces(files []string) (map[string]*ast.InterfaceType, error) {
	specs := map[string]*ast.InterfaceType{}

	for _, file := range files {
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			return nil, fmt.Errorf("cannot parse %s: %w", file, err)
		}

		for _, decl := range parsed.Decls {
			collect(decl, specs)
		}
	}

	return specs, nil
}

func collect(decl ast.Decl, specs map[string]*ast.InterfaceType) {
	gen, ok := decl.(*ast.GenDecl)
	if !ok {
		return
	}

	for _, spec := range gen.Specs {
		if typed, isType := spec.(*ast.TypeSpec); isType {
			if body, isInterface := typed.Type.(*ast.InterfaceType); isInterface {
				specs[typed.Name.Name] = body
			}
		}
	}
}

func add(specs map[string]*ast.InterfaceType, name string, found map[string]bool) error {
	body, ok := specs[name]
	if !ok {
		return fmt.Errorf("no interface %s in the files", name)
	}

	for _, field := range body.Methods.List {
		if len(field.Names) > 0 {
			found[field.Names[0].Name] = true

			continue
		}

		if embedded, isIdent := field.Type.(*ast.Ident); isIdent {
			if err := add(specs, embedded.Name, found); err != nil {
				return err
			}
		}
	}

	return nil
}
