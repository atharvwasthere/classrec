// Package fsx holds the one file helper several packages share.
package fsx

import (
	"os"
	"path/filepath"
)

// WriteFileAtomic writes data to a temp file and renames it into place, so a crash never leaves a half-written file.
func WriteFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".part"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
