package main

import (
	"io"
	"os"
	"path/filepath"
)

// version is the release tag, set at build time with -ldflags "-X main.version=v0.1.0".
var version = "dev"

// reportsDir is where reports go: OneDrive, so Adi can read them from anywhere, else the
// recordings folder. Reports never include transcripts or recordings.
func reportsDir(root string) string {
	if od := os.Getenv("OneDrive"); od != "" {
		if _, err := os.Stat(od); err == nil {
			return filepath.Join(od, "classrec-reports")
		}
	}
	return filepath.Join(root, "_reports")
}

// copyToReports copies a report file into the reports folder, as name ("" keeps its own); a
// failure only loses the copy.
func copyToReports(root, path, name string) {
	if name == "" {
		name = filepath.Base(path)
	}
	dir := reportsDir(root)
	if os.MkdirAll(dir, 0o755) != nil {
		return
	}
	in, err := os.Open(path)
	if err != nil {
		return
	}
	defer in.Close()
	out, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		return
	}
	defer out.Close()
	_, _ = io.Copy(out, in)
}
