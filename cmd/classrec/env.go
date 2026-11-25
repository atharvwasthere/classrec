package main

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// exeDir is the folder holding the classrec binary, wherever classrec is run from; "." if unknown.
func exeDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return filepath.Dir(exe)
}

// defaultEnvFile is the key file next to the classrec binary.
func defaultEnvFile() string { return filepath.Join(exeDir(), ".env") }

// loadAPIKey returns SARVAM_API_KEY from the environment, else from the env file.
// The value is returned to the caller only; it is never printed or logged.
func loadAPIKey(envFile string) (string, error) {
	if k := strings.TrimSpace(os.Getenv("SARVAM_API_KEY")); k != "" {
		return k, nil
	}
	f, err := os.Open(envFile)
	if err != nil {
		return "", errors.New("SARVAM_API_KEY is not set: export it, or put it in " + envFile)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if k, ok := parseKeyLine(sc.Text()); ok {
			return k, nil
		}
	}
	return "", errors.New("SARVAM_API_KEY not found in " + envFile)
}

func parseKeyLine(line string) (string, bool) {
	line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
	name, val, found := strings.Cut(line, "=")
	if !found || strings.TrimSpace(name) != "SARVAM_API_KEY" {
		return "", false
	}
	val = strings.TrimSpace(val)
	if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') && val[len(val)-1] == val[0] {
		val = val[1 : len(val)-1]
	}
	return val, val != ""
}
