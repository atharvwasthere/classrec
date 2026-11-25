package overlay

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSavePositionKeepsOtherSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(path, []byte(`{"mode": "full"}`), 0o644)
	if err := savePosition(path, point{-1200, 300}); err != nil {
		t.Fatal(err)
	}
	var s struct{ Mode string }
	b, _ := os.ReadFile(path)
	json.Unmarshal(b, &s)
	if p, ok := loadPosition(path); !ok || p != (point{-1200, 300}) || s.Mode != "full" {
		t.Fatalf("got %s", b)
	}
}

func TestSavePositionLeavesBrokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(path, []byte(`{"mode": "full",}`), 0o644)
	if err := savePosition(path, point{1, 2}); err == nil {
		t.Fatal("want error")
	}
	if b, _ := os.ReadFile(path); string(b) != `{"mode": "full",}` {
		t.Fatalf("file was overwritten: %s", b)
	}
}
