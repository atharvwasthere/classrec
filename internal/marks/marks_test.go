package marks

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "class", "marks.json")
	for _, s := range []float64{12.34, 600} {
		if err := Append(path, s); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(path)
	if string(b) != "[12.3,600]" {
		t.Fatalf("got %s", b)
	}
}

func TestAppendKeepsCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "marks.json")
	os.WriteFile(path, []byte("not json"), 0o644)
	if err := Append(path, 1); err == nil {
		t.Fatal("want error")
	}
	if b, _ := os.ReadFile(path); string(b) != "not json" {
		t.Fatalf("file was overwritten: %s", b)
	}
}
