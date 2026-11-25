package recorder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"classrec/internal/fsx"
)

// MoveOptions says how long and how often to retry a move while OBS may still hold the file.
// The zero value means the defaults: retry for 60 s, waiting 0.5 s at first and doubling up to 5 s.
type MoveOptions struct {
	Total   time.Duration
	First   time.Duration
	Longest time.Duration

	rename func(from, to string) error                      // os.Rename, replaced in tests
	sleep  func(ctx context.Context, d time.Duration) error // sleepCtx, replaced in tests
}

func (o MoveOptions) withDefaults() MoveOptions {
	if o.Total == 0 {
		o.Total = 60 * time.Second
	}
	if o.First == 0 {
		o.First = 500 * time.Millisecond
	}
	if o.Longest == 0 {
		o.Longest = 5 * time.Second
	}
	if o.rename == nil {
		o.rename = os.Rename
	}
	if o.sleep == nil {
		o.sleep = sleepCtx
	}
	return o
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// moveFile moves src to dst and returns where it ended up. It never overwrites: if dst exists the
// file goes to "name (2).ext", "name (3).ext" and so on. A failed rename, normally OBS still holding
// the file, is retried with growing waits until opts.Total has passed. It never deletes src.
func moveFile(ctx context.Context, src, dst string, opts MoveOptions) (string, error) {
	opts = opts.withDefaults()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	wait := opts.First
	var waited time.Duration
	for {
		target, err := freeName(dst)
		if err == nil {
			err = opts.rename(src, target)
			if err == nil {
				return target, nil
			}
		}
		if _, statErr := os.Stat(src); errors.Is(statErr, os.ErrNotExist) {
			return "", fmt.Errorf("%s is gone: %w", src, err)
		}
		if waited >= opts.Total {
			return "", fmt.Errorf("could not move %s after %s: %w", src, waited, err)
		}
		if err := opts.sleep(ctx, wait); err != nil {
			return "", err
		}
		waited += wait
		wait = min(wait*2, opts.Longest)
	}
}

// freeName returns path, or the first "name (n).ext" that does not exist yet.
func freeName(path string) (string, error) {
	ext := filepath.Ext(path)
	stem := strings.TrimSuffix(path, ext)
	candidate := path
	for n := 2; ; n++ {
		_, err := os.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		}
		if err != nil {
			return "", err
		}
		candidate = fmt.Sprintf("%s (%d)%s", stem, n, ext)
	}
}

// pendingMove is a file that could not be moved and is still in the staging folder.
type pendingMove struct {
	Src string `json:"src"`
	Dst string `json:"dst"`
}

const pendingFile = "pending.json"

// pendingMu serializes changes to pending.json: classes filed on different goroutines and the
// start-up retry all edit it.
var pendingMu sync.Mutex

func readPending(staging string) []pendingMove {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	return readPendingLocked(staging)
}

func readPendingLocked(staging string) []pendingMove {
	content, err := os.ReadFile(filepath.Join(staging, pendingFile))
	if err != nil {
		return nil
	}
	var list []pendingMove
	if json.Unmarshal(content, &list) != nil {
		return nil
	}
	return list
}

func writePendingLocked(staging string, list []pendingMove) error {
	content, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return fsx.WriteFileAtomic(filepath.Join(staging, pendingFile), content)
}

func addPending(staging string, m pendingMove) error {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	list := readPendingLocked(staging)
	for _, p := range list {
		if p == m {
			return nil
		}
	}
	return writePendingLocked(staging, append(list, m))
}

// removePending drops entries that no longer need a retry, because they were filed or their file is gone.
func removePending(staging string, done []pendingMove) error {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	var keep []pendingMove
	for _, p := range readPendingLocked(staging) {
		finished := false
		for _, d := range done {
			finished = finished || p == d
		}
		if !finished {
			keep = append(keep, p)
		}
	}
	return writePendingLocked(staging, keep)
}
