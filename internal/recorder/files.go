package recorder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"classrec/internal/detect"
	"classrec/internal/fsx"
)

// The files of a class folder besides its video. PiecesFile says when each piece began, so the
// pieces can be joined in order; oldPiecesFile is its name in folders filed before v0.1.12.
const (
	PiecesFile    = "Recording parts.json"
	LogFile       = "Recording log.txt"
	oldPiecesFile = "pieces.json"
)

// ReadPieces reads a class folder's PiecesFile, or the pieces.json of an older folder.
func ReadPieces(dir string) (Pieces, error) {
	var info Pieces
	content, err := os.ReadFile(filepath.Join(dir, PiecesFile))
	if errors.Is(err, os.ErrNotExist) {
		content, err = os.ReadFile(filepath.Join(dir, oldPiecesFile))
	}
	if err != nil {
		return info, err
	}
	if err := json.Unmarshal(content, &info); err != nil {
		return info, fmt.Errorf("%s: %w", PiecesFile, err)
	}
	return info, nil
}

// IsClassFolder reports whether dir is a class folder the agent filed.
func IsClassFolder(dir string) bool {
	for _, name := range []string{PiecesFile, oldPiecesFile} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}

// pieceName names a class's video after its folder, so a copy taken out of the folder still says
// which class it is: "<folder>.mkv", or "<folder> - part 2.mkv" when OBS was restarted mid-class.
func pieceName(dir string, i, count int, ext string) string {
	if count == 1 {
		return filepath.Base(dir) + ext
	}
	return fmt.Sprintf("%s - part %d%s", filepath.Base(dir), i+1, ext)
}

// Pieces is the content of PiecesFile.
type Pieces struct {
	App    detect.App  `json:"app"`
	Title  string      `json:"title"`
	Began  time.Time   `json:"began"`
	Pieces []PieceInfo `json:"pieces"`
}

// PieceInfo is one recording file of a class.
type PieceInfo struct {
	File  string    `json:"file"`
	Start time.Time `json:"start"` // wall clock, when OBS began writing it
}

// file makes the class folder, notes when each piece began, and moves the pieces in. A piece OBS
// still holds after moveTimeout stays in staging and goes into pending.json for RetryPending.
func (r *Recorder) file(ctx context.Context) (Class, error) {
	dir, err := makeClassDir(r.cfg.Root, classFolderName(r.began, r.target.App, r.name))
	if err != nil {
		return Class{}, fmt.Errorf("making the class folder: %w", err)
	}
	class := Class{Dir: dir}
	info := Pieces{App: r.target.App, Title: r.name, Began: r.began}
	for i, p := range r.pieces {
		name := pieceName(dir, i, len(r.pieces), filepath.Ext(p.path))
		info.Pieces = append(info.Pieces, PieceInfo{File: name, Start: p.start})
		dst := filepath.Join(dir, name)
		moved, err := r.move(ctx, p.path, dst)
		if err != nil {
			r.cfg.Log("leaving %s in staging: %v", p.path, err)
			if err := addPending(r.staging(), pendingMove{Src: p.path, Dst: dst}); err != nil {
				r.cfg.Log("could not note the pending move: %v", err)
			}
			class.Pending++
			continue
		}
		class.Files = append(class.Files, moved)
	}
	content, err := json.MarshalIndent(info, "", "  ")
	if err == nil {
		err = fsx.WriteFileAtomic(filepath.Join(dir, PiecesFile), content)
	}
	return class, err
}

// RetryPending moves files an earlier run left in staging. The agent calls it when it starts.
func (r *Recorder) RetryPending(ctx context.Context) (moved, still int) {
	var keep []pendingMove
	for _, p := range readPending(r.staging()) {
		if _, err := os.Stat(p.Src); err != nil {
			continue // already filed, or moved by hand
		}
		if _, err := r.move(ctx, p.Src, p.Dst); err != nil {
			r.cfg.Log("still cannot move %s: %v", p.Src, err)
			keep = append(keep, p)
			continue
		}
		moved++
	}
	if err := writePending(r.staging(), keep); err != nil {
		r.cfg.Log("could not update the pending list: %v", err)
	}
	return moved, len(keep)
}

// move renames src to dst, or to "dst (2)" and so on if dst exists: it never overwrites and
// never deletes. While OBS still holds the file the rename fails, so it retries for moveTimeout.
func (r *Recorder) move(ctx context.Context, src, dst string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	wait, waited := 500*time.Millisecond, time.Duration(0)
	for {
		target, err := freeName(dst)
		if err == nil {
			if err = r.cfg.rename(src, target); err == nil {
				return target, nil
			}
		}
		if waited >= moveTimeout {
			return "", fmt.Errorf("could not move it in %s: %w", moveTimeout, err)
		}
		if err := r.cfg.Sleep(ctx, wait); err != nil {
			return "", err
		}
		waited += wait
		wait = min(wait*2, 2*time.Second)
	}
}

// freeName returns path, or the first "name (n).ext" that does not exist yet.
func freeName(path string) (string, error) {
	ext := filepath.Ext(path)
	candidate := path
	for n := 2; ; n++ {
		_, err := os.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		}
		if err != nil {
			return "", err
		}
		candidate = fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(path, ext), n, ext)
	}
}

// pendingMove is a piece still in staging, and where it belongs.
type pendingMove struct {
	Src string `json:"src"`
	Dst string `json:"dst"`
}

func pendingPath(staging string) string { return filepath.Join(staging, "pending.json") }

func readPending(staging string) []pendingMove {
	var list []pendingMove
	if content, err := os.ReadFile(pendingPath(staging)); err == nil {
		_ = json.Unmarshal(content, &list) // an unreadable list holds nothing to retry
	}
	return list
}

func writePending(staging string, list []pendingMove) error {
	content, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return fsx.WriteFileAtomic(pendingPath(staging), content)
}

func addPending(staging string, m pendingMove) error {
	return writePending(staging, append(readPending(staging), m))
}

// classFolderName is "<YYYY-MM-DD> <Day> - <title> (<Zoom|Meet>)".
func classFolderName(began time.Time, app detect.App, windowTitle string) string {
	name := fmt.Sprintf("%s %s - %s", began.Format("2006-01-02"), began.Weekday(), sanitizeTitle(windowTitle))
	if app == "" {
		return name // a recording left running: which app it was is not known
	}
	return fmt.Sprintf("%s (%s)", name, app)
}

// otherTabs is what Edge adds to a window title when the window has more tabs: " and 2 more pages".
var otherTabs = regexp.MustCompile(` and \d+ more pages?\b`)

// sanitizeTitle turns a meeting window title into a short, safe folder name part: the "Meet - "
// and "Zoom Meeting" boilerplate, Edge's suffix and the profile name before it are dropped;
// \ / : * ? " < > | and control characters are removed; spaces are collapsed; dots, spaces and
// dashes are trimmed from the ends; at most 60 characters are kept; empty becomes "Class".
func sanitizeTitle(title string) string {
	title = otherTabs.ReplaceAllString(title, "")
	parts := strings.Split(title, " - ")
	last := strings.ReplaceAll(strings.TrimSpace(parts[len(parts)-1]), "\u200b", "") // Edge writes "Microsoft\u200b Edge"
	edge := strings.EqualFold(last, "Microsoft Edge")
	if edge {
		parts = parts[:len(parts)-1]
	}
	if len(parts) > 0 && strings.EqualFold(strings.TrimSpace(parts[0]), "Meet") {
		parts = parts[1:]
	}
	if edge && len(parts) >= 2 {
		parts = parts[:len(parts)-1] // Edge's profile name, e.g. "Personal"
	}
	text := removeFold(strings.Join(parts, " - "), "Zoom Meeting")
	text = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`\/:*?"<>|`, r) {
			return -1
		}
		return r
	}, text)
	text = trimEnds(strings.Join(strings.Fields(text), " "))
	if utf8.RuneCountInString(text) > 60 {
		text = trimEnds(string([]rune(text)[:60]))
	}
	if text == "" {
		return "Class"
	}
	return text
}

func trimEnds(s string) string { return strings.Trim(s, ". -") }

// removeFold deletes every case-insensitive occurrence of word.
func removeFold(text, word string) string {
	for {
		lower := strings.ToLower(text)
		i := strings.Index(lower, strings.ToLower(word))
		if i < 0 || len(lower) != len(text) {
			return text
		}
		text = text[:i] + text[i+len(word):]
	}
}

// makeClassDir creates a new folder under root, adding " (2)" and so on if the name is taken, so
// two classes never share a folder.
func makeClassDir(root, name string) (string, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	candidate := filepath.Join(root, name)
	for n := 2; ; n++ {
		err := os.Mkdir(candidate, 0o755)
		if err == nil || !os.IsExist(err) {
			return candidate, err
		}
		candidate = filepath.Join(root, fmt.Sprintf("%s (%d)", name, n))
	}
}
