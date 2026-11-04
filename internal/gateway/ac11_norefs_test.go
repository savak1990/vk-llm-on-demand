package gateway

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This package's own non-test source must never call .Start(, .Stop(
// or .Demand( on anything — a guard against the gateway ever
// provisioning, stopping, or registering demand directly.
func TestNoProvisioningCalls(t *testing.T) {
	forbidden := map[string]bool{"Start": true, "Stop": true, "Demand": true}
	fset := token.NewFileSet()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), "_test.go") || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(".", e.Name()), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", e.Name(), err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			// Every provisioning method takes at least a context argument;
			// a nullary Stop (e.g. a ticker) is not one of them.
			if forbidden[sel.Sel.Name] && len(call.Args) > 0 {
				t.Errorf("%s: forbidden call .%s( found", e.Name(), sel.Sel.Name)
			}
			return true
		})
	}
}

func TestControllerInterfaceHasExactlyFiveMethods(t *testing.T) {
	var methods []string
	typ := (*Controller)(nil)
	_ = typ // the interface's method set is checked structurally below

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "gateway.go", nil, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != "Controller" {
			return true
		}
		it, ok := ts.Type.(*ast.InterfaceType)
		if !ok {
			return true
		}
		for _, m := range it.Methods.List {
			for _, name := range m.Names {
				methods = append(methods, name.Name)
			}
		}
		return true
	})
	if len(methods) != 5 {
		t.Fatalf("Controller interface has %d methods (%v), want 5", len(methods), methods)
	}
}
