package httpserver

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

func TestOpenAPIDocumentValidatesAndMatchesRegisteredRoutes(t *testing.T) {
	root := repositoryRoot(t)
	loader := openapi3.NewLoader()
	document, err := loader.LoadFromFile(filepath.Join(root, "api", "openapi", "control-plane.yaml"))
	if err != nil {
		t.Fatalf("load OpenAPI document: %v", err)
	}
	if err := document.Validate(context.Background()); err != nil {
		t.Fatalf("validate OpenAPI document: %v", err)
	}
	documented := map[string]struct{}{}
	for path, item := range document.Paths.Map() {
		for method, operation := range map[string]*openapi3.Operation{
			"GET": item.Get, "POST": item.Post, "PATCH": item.Patch, "DELETE": item.Delete,
		} {
			if operation == nil {
				continue
			}
			if strings.TrimSpace(operation.OperationID) == "" {
				t.Fatalf("%s %s has no operationId", method, path)
			}
			documented[method+" "+path] = struct{}{}
		}
	}
	registered := registeredRouteCatalog(t, root)
	for route := range registered {
		if _, ok := documented[route]; !ok {
			t.Errorf("registered route is undocumented: %s", route)
		}
	}
	for route := range documented {
		if _, ok := registered[route]; !ok {
			t.Errorf("documented Phase 2 route is not registered: %s", route)
		}
	}
}

func registeredRouteCatalog(t *testing.T, root string) map[string]struct{} {
	t.Helper()
	files := []struct {
		path   string
		prefix string
	}{
		{path: filepath.Join(root, "internal", "platform", "httpserver", "router.go")},
		{path: filepath.Join(root, "internal", "tenancy", "http.go"), prefix: "/api/v1"},
		{path: filepath.Join(root, "internal", "identity", "http.go"), prefix: "/api/v1"},
		{path: filepath.Join(root, "internal", "audit", "http.go"), prefix: "/api/v1"},
		{path: filepath.Join(root, "internal", "accounts", "http.go"), prefix: "/api/v1"},
		{path: filepath.Join(root, "internal", "pools", "http.go"), prefix: "/api/v1"},
	}
	methods := map[string]string{"Get": "GET", "Post": "POST", "Patch": "PATCH", "Delete": "DELETE"}
	routes := map[string]struct{}{}
	for _, file := range files {
		parsed, err := parser.ParseFile(token.NewFileSet(), file.path, nil, 0)
		if err != nil {
			t.Fatalf("parse route source %s: %v", file.path, err)
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			receiver, ok := selector.X.(*ast.Ident)
			if !ok || (receiver.Name != "router" && receiver.Name != "r") {
				return true
			}
			method, ok := methods[selector.Sel.Name]
			if !ok {
				return true
			}
			literal, ok := call.Args[0].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				t.Errorf("route path for %s in %s is not a string literal", selector.Sel.Name, file.path)
				return true
			}
			path, err := strconv.Unquote(literal.Value)
			if err != nil {
				t.Errorf("decode route path %s: %v", literal.Value, err)
				return true
			}
			routes[fmt.Sprintf("%s %s%s", method, file.prefix, path)] = struct{}{}
			return true
		})
	}
	return routes
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate OpenAPI test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
}
