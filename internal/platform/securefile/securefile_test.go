package securefile_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/znicelya/ajiasu-proxy/internal/platform/securefile"
)

func TestReadRejectsUnsafeInputsWithoutExposingPath(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name  string
		setup func(string) error
	}{
		{name: "missing", setup: func(string) error { return nil }},
		{name: "directory", setup: func(path string) error { return os.Mkdir(path, 0o700) }},
		{name: "empty", setup: func(path string) error { return os.WriteFile(path, nil, 0o600) }},
		{name: "oversize", setup: func(path string) error { return os.WriteFile(path, []byte("12345"), 0o600) }},
	}
	if runtime.GOOS != "windows" {
		tests = append(tests, struct {
			name  string
			setup func(string) error
		}{name: "broad permissions", setup: func(path string) error { return os.WriteFile(path, []byte("safe"), 0o640) }})
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(root, test.name)
			if err := test.setup(path); err != nil {
				t.Fatal(err)
			}
			_, err := securefile.Read(path, 1, 4, true)
			if !errors.Is(err, securefile.ErrInvalid) {
				t.Fatalf("Read() error=%v", err)
			}
			if strings.Contains(err.Error(), path) {
				t.Fatalf("Read() exposed path: %v", err)
			}
		})
	}
}

func TestReadRejectsSymbolicLink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	link := filepath.Join(root, "link")
	if err := os.WriteFile(target, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	if _, err := securefile.Read(link, 1, 16, true); !errors.Is(err, securefile.ErrInvalid) {
		t.Fatalf("Read(symlink) error=%v", err)
	}
}

func TestReadAcceptsPrivateRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	content, err := securefile.Read(path, 1, 16, true)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "secret" {
		t.Fatalf("content=%q", content)
	}
}
