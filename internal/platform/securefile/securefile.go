package securefile

import (
	"errors"
	"io"
	"os"
	"runtime"
)

var ErrInvalid = errors.New("secure file is unavailable")

// Read reads one bounded regular file without following a symbolic link. The
// returned error never includes the path or file content.
func Read(path string, minimum, maximum int64, private bool) ([]byte, error) {
	if path == "" || minimum < 0 || maximum < minimum {
		return nil, ErrInvalid
	}
	before, err := os.Lstat(path)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return nil, ErrInvalid
	}
	if private && runtime.GOOS != "windows" && before.Mode().Perm()&0o077 != 0 {
		return nil, ErrInvalid
	}
	if before.Size() < minimum || before.Size() > maximum {
		return nil, ErrInvalid
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, ErrInvalid
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return nil, ErrInvalid
	}
	content, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(content)) < minimum || int64(len(content)) > maximum {
		clear(content)
		return nil, ErrInvalid
	}
	return content, nil
}
