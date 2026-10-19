package database

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"slices"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

func typeNames(objects []client.Object) []string {
	names := make([]string, 0, len(objects))
	for _, object := range objects {
		names = append(names, reflect.TypeOf(object).Elem().Name())
	}
	slices.Sort(names)
	return slices.Compact(names)
}

func TestCachedTypesAreExactlyWhatTheControllersWatch(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "setup.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var used []string
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, isSelector := call.Fun.(*ast.SelectorExpr)
		if !isSelector || (selector.Sel.Name != "For" && selector.Sel.Name != "Owns" && selector.Sel.Name != "Watches") || len(call.Args) == 0 {
			return true
		}
		if unary, ok := call.Args[0].(*ast.UnaryExpr); ok {
			if literal, ok := unary.X.(*ast.CompositeLit); ok {
				used = append(used, typeIdent(literal.Type))
			}
		}
		return true
	})
	slices.Sort(used)
	used = slices.Compact(used)
	if want := typeNames(CachedTypes()); !slices.Equal(used, want) {
		t.Fatalf("setup.go watches %v, CachedTypes lists %v", used, want)
	}
	if slices.Contains(used, "Secret") {
		t.Fatal("the controllers must not start a Secret informer")
	}
}

func typeIdent(expression ast.Expr) string {
	if selector, ok := expression.(*ast.SelectorExpr); ok {
		return selector.Sel.Name
	}
	return ""
}
