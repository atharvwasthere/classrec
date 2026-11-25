package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAgentMode(t *testing.T) {
	tests := []struct {
		name, file, flag, want string
		wantErr                bool
	}{
		{"no file", "", "", modeRecord, false},
		{"full in file", `{"mode": "full"}`, "", modeFull, false},
		{"record in file", `{"mode": "record"}`, "", modeRecord, false},
		{"unknown mode", `{"mode": "Full"}`, "", modeRecord, false},
		{"mode not a string", `{"mode": 1}`, "", modeRecord, false},
		{"broken json", `{"mode": "full",}`, "", modeRecord, false},
		{"no mode key", `{"overlay": {"X": 1, "Y": 2}}`, "", modeRecord, false},
		{"flag beats file", `{"mode": "full"}`, "record", modeRecord, false},
		{"flag full", "", "full", modeFull, false},
		{"bad flag", "", "fulll", "", true},
	}
	for _, tt := range tests {
		path := filepath.Join(t.TempDir(), "settings.json")
		if tt.file != "" {
			os.WriteFile(path, []byte(tt.file), 0o644)
		}
		got, source, err := agentMode(tt.flag, path)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("%s: got %q (%s), err %v; want %q", tt.name, got, source, err, tt.want)
		}
	}
}
