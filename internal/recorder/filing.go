package recorder

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"classrec/internal/fsx"
)

const (
	maxTitleRunes = 60
	fallbackTitle = "Class"

	// PiecesFile in a class folder says when each piece began, so a mark made at a wall-clock
	// time can be found in the joined recording.
	PiecesFile = "pieces.json"
)

// ClassFolderName is "<YYYY-MM-DD> <Day> - <title> (<Zoom|Meet>)".
func ClassFolderName(began time.Time, app, windowTitle string) string {
	return fmt.Sprintf("%s %s - %s (%s)", began.Format("2006-01-02"), began.Weekday(), SanitizeTitle(windowTitle), app)
}

// SanitizeTitle turns a meeting window title into something safe and short for a folder name:
// the "Meet - " and "Zoom Meeting" boilerplate, Edge's own suffix and the profile name before
// it are dropped; \ / : * ? " < > | and control characters are removed; spaces are collapsed;
// trailing dots and spaces are trimmed; at most 60 characters are kept. An empty result is "Class".
func SanitizeTitle(title string) string {
	parts := strings.Split(title, " - ")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}

	edge := len(parts) > 0 && strings.EqualFold(parts[len(parts)-1], "Microsoft​ Edge") ||
		len(parts) > 0 && strings.EqualFold(parts[len(parts)-1], "Microsoft Edge")
	if edge {
		parts = parts[:len(parts)-1]
	}
	if len(parts) > 0 && strings.EqualFold(parts[0], "Meet") {
		parts = parts[1:]
	}
	if edge && len(parts) >= 2 {
		parts = parts[:len(parts)-1] // Edge's profile name, e.g. "Personal"
	}

	text := strings.Join(parts, " - ")
	text = removeFold(text, "Zoom Meeting")
	text = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`\/:*?"<>|`, r) {
			return -1
		}
		return r
	}, text)
	text = strings.Join(strings.Fields(text), " ")
	text = trimEnds(text)
	if utf8.RuneCountInString(text) > maxTitleRunes {
		text = string([]rune(text)[:maxTitleRunes])
		text = trimEnds(text)
	}
	if text == "" {
		return fallbackTitle
	}
	return text
}

// trimEnds drops dots and spaces from the end (Windows does not allow them there), and the
// hyphens and spaces left at either end when boilerplate was cut out of "Zoom Meeting - Algebra".
func trimEnds(s string) string { return strings.Trim(s, ". -") }

// removeFold deletes every case-insensitive occurrence of word.
func removeFold(text, word string) string {
	lower, lowerWord := strings.ToLower(text), strings.ToLower(word)
	var out strings.Builder
	for {
		i := strings.Index(lower, lowerWord)
		if i < 0 || len(lower) != len(text) { // lengths differ only for odd Unicode; give up on those
			out.WriteString(text)
			return out.String()
		}
		out.WriteString(text[:i])
		text, lower = text[i+len(word):], lower[i+len(word):]
	}
}

// makeClassDir creates a new folder for the class under root, adding " (2)" and so on if one with
// that name exists, so two classes never share a folder.
func makeClassDir(root, name string) (string, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	candidate := filepath.Join(root, name)
	for n := 2; ; n++ {
		err := os.Mkdir(candidate, 0o755)
		if err == nil {
			return candidate, nil
		}
		if !os.IsExist(err) {
			return "", err
		}
		candidate = filepath.Join(root, fmt.Sprintf("%s (%d)", name, n))
	}
}

type piecesFile struct {
	App    string      `json:"app"`
	Title  string      `json:"title"`
	Began  time.Time   `json:"began"`
	Pieces []pieceInfo `json:"pieces"`
}

type pieceInfo struct {
	File  string    `json:"file"`
	Start time.Time `json:"start"` // wall clock, when OBS began writing this piece
}

// startFiling makes the class folder, notes when each piece began, and moves the pieces in the
// background. The folder and pieces.json exist when it returns; the files arrive as OBS releases them.
func (r *Recorder) startFiling(ctx context.Context, target Target, began time.Time, pieces []piece) (*Filing, error) {
	dir, err := makeClassDir(r.cfg.Root, ClassFolderName(began, target.App, target.Title))
	if err != nil {
		return nil, fmt.Errorf("making the class folder: %w", err)
	}
	filing := &Filing{Dir: dir, done: make(chan struct{})}

	info := piecesFile{App: target.App, Title: target.Title, Began: began}
	destinations := make([]string, len(pieces))
	for i, p := range pieces {
		name := fmt.Sprintf("part-%02d%s", i+1, filepath.Ext(p.Path))
		destinations[i] = filepath.Join(dir, name)
		info.Pieces = append(info.Pieces, pieceInfo{File: name, Start: p.Start})
	}
	content, err := json.MarshalIndent(info, "", "  ")
	if err == nil {
		err = fsx.WriteFileAtomic(filepath.Join(dir, PiecesFile), content)
	}
	if err != nil {
		r.cfg.Log("could not write %s: %v", PiecesFile, err)
	}

	ctx = context.WithoutCancel(ctx) // finishing the class must not die with the caller's context
	r.filings.Add(1)
	go func() {
		defer r.filings.Done()
		defer close(filing.done)
		filing.class.Dir = dir
		for i, p := range pieces {
			moved, err := moveFile(ctx, p.Path, destinations[i], r.moveOptions())
			if err != nil {
				r.cfg.Log("leaving %s in staging: %v", p.Path, err)
				if perr := addPending(r.Staging(), pendingMove{Src: p.Path, Dst: destinations[i]}); perr != nil {
					r.cfg.Log("could not note the pending move: %v", perr)
				}
				filing.class.Pending++
				continue
			}
			filing.class.Files = append(filing.class.Files, moved)
		}
	}()
	return filing, nil
}
