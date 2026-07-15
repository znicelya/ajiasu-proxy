package security

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPhase5ProxySecretLogCanary(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	for _, path := range []string{filepath.Join(root, "crates", "gateway"), filepath.Join(root, "crates", "agent"), filepath.Join(root, "internal", "proxyaccess")} {
		_ = filepath.Walk(path, func(file string, info os.FileInfo, err error) error {
			if err != nil || info == nil || info.IsDir() {
				return nil
			}
			data, readErr := os.ReadFile(file)
			if readErr != nil {
				return nil
			}
			text := strings.ToLower(string(data))
			if strings.Contains(text, "println!(\"{}\", password") || strings.Contains(text, "log.password") {
				t.Errorf("secret logging pattern in %s", file)
			}
			return nil
		})
	}
}
