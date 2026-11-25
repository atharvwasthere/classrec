package recorder

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMoveNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "new.mkv")
	dst := filepath.Join(dir, "class", "part-01.mkv")
	writeFile(t, src, "new")
	writeFile(t, dst, "old")
	writeFile(t, filepath.Join(dir, "class", "part-01 (2).mkv"), "older")

	got, err := moveFile(context.Background(), src, dst, MoveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "class", "part-01 (3).mkv"); got != want {
		t.Fatalf("moved to %q, want %q", got, want)
	}
	if readFile(t, dst) != "old" || readFile(t, filepath.Join(dir, "class", "part-01 (2).mkv")) != "older" || readFile(t, got) != "new" {
		t.Fatal("an existing file changed")
	}
	if _, err := os.Stat(src); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("source still there after the move")
	}
}

func TestMoveRetriesWithGrowingWaits(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "a.mkv"), filepath.Join(dir, "out", "a.mkv")
	writeFile(t, src, "x")
	var failures int
	var waits []time.Duration
	opts := MoveOptions{
		Total: time.Minute, First: 500 * time.Millisecond, Longest: 2 * time.Second,
		rename: func(from, to string) error {
			if failures < 5 {
				failures++
				return errors.New("sharing violation")
			}
			return os.Rename(from, to)
		},
		sleep: func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil },
	}
	if _, err := moveFile(context.Background(), src, dst, opts); err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 2 * time.Second, 2 * time.Second}
	if len(waits) != len(want) {
		t.Fatalf("waits %v", waits)
	}
	for i := range want {
		if waits[i] != want[i] {
			t.Fatalf("waits %v, want %v", waits, want)
		}
	}
}

func TestMoveGivesUpButKeepsTheFile(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "a.mkv"), filepath.Join(dir, "out", "a.mkv")
	writeFile(t, src, "precious")
	opts := MoveOptions{
		Total: 3 * time.Second, First: time.Second,
		rename: func(string, string) error { return errors.New("sharing violation") },
		sleep:  func(context.Context, time.Duration) error { return nil },
	}
	if _, err := moveFile(context.Background(), src, dst, opts); err == nil {
		t.Fatal("expected an error")
	}
	if readFile(t, src) != "precious" {
		t.Fatal("the source was touched")
	}
}
