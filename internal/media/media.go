// Package media shells out to ffmpeg/ffprobe; no cgo.
package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Silence struct {
	Start, End float64
}

var exeDir = sync.OnceValue(func() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return filepath.Dir(exe)
})

// resolveBin prefers ffmpeg/ffprobe sitting next to the classrec binary, then falls back to PATH.
func resolveBin(name string) string { return resolveIn(exeDir(), name, runtime.GOOS) }

func resolveIn(dir, name, goos string) string {
	if dir == "" {
		return name
	}
	file := name
	if goos == "windows" {
		file += ".exe"
	}
	candidate := filepath.Join(dir, file)
	if fi, err := os.Stat(candidate); err == nil && !fi.IsDir() {
		return candidate
	}
	return name
}

func run(ctx context.Context, name string, args ...string) (stdout, stderr string, err error) {
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, resolveBin(name), args...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), errb.String(), fmt.Errorf("%s: %w: %s", name, err, tail(errb.String()))
	}
	return out.String(), errb.String(), nil
}

func tail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 600 {
		s = s[len(s)-600:]
	}
	return s
}

// Probe returns the container duration in seconds.
func Probe(ctx context.Context, path string) (float64, error) {
	out, _, err := run(ctx, "ffprobe", "-v", "error", "-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1", path)
	if err != nil {
		return 0, err
	}
	d, err := strconv.ParseFloat(strings.TrimSpace(out), 64)
	if err != nil {
		return 0, fmt.Errorf("ffprobe duration %q: %w", strings.TrimSpace(out), err)
	}
	return d, nil
}

// ExtractAudio writes mono opus 32 kbps. It writes to a temp file and renames, so a crash never leaves a half file.
func ExtractAudio(ctx context.Context, in, out string) error {
	tmp := out + ".part"
	defer os.Remove(tmp)
	if _, _, err := run(ctx, "ffmpeg", "-y", "-v", "error", "-i", in, "-vn", "-ac", "1",
		"-c:a", "libopus", "-b:a", "32k", "-f", "ogg", tmp); err != nil {
		return err
	}
	return os.Rename(tmp, out)
}

var (
	silenceStart = regexp.MustCompile(`silence_start: (-?[0-9.]+)`)
	silenceEnd   = regexp.MustCompile(`silence_end: (-?[0-9.]+)`)
)

// parseSilences reads ffmpeg silencedetect stderr. A silence still open at EOF is dropped.
func parseSilences(stderr string) []Silence {
	var res []Silence
	open := -1.0
	for _, line := range strings.Split(stderr, "\n") {
		if m := silenceStart.FindStringSubmatch(line); m != nil {
			open, _ = strconv.ParseFloat(m[1], 64)
		}
		if m := silenceEnd.FindStringSubmatch(line); m != nil && open >= 0 {
			end, _ := strconv.ParseFloat(m[1], 64)
			res = append(res, Silence{Start: open, End: end})
			open = -1
		}
	}
	return res
}

func DetectSilences(ctx context.Context, audio string, noiseDB, minDur float64) ([]Silence, error) {
	filter := fmt.Sprintf("silencedetect=noise=%gdB:d=%g", noiseDB, minDur)
	_, stderr, err := run(ctx, "ffmpeg", "-v", "info", "-nostats", "-i", audio, "-af", filter, "-f", "null", "-")
	if err != nil {
		return nil, err
	}
	return parseSilences(stderr), nil
}

// Cut re-encodes [start, start+dur) to a standalone opus file. Stream copy is not an option:
// ogg/opus copy either starts up to a page early or writes negative granule positions (invalid
// files that Sarvam rejects), so only a re-encode is both valid and sample-accurate.
func Cut(ctx context.Context, in, out string, start, dur float64) error {
	tmp := out + ".part"
	defer os.Remove(tmp)
	if _, _, err := run(ctx, "ffmpeg", "-y", "-v", "error", "-ss", fmt.Sprintf("%.3f", start),
		"-i", in, "-t", fmt.Sprintf("%.3f", dur), "-ac", "1", "-c:a", "libopus", "-b:a", "32k", "-f", "ogg", tmp); err != nil {
		return err
	}
	return os.Rename(tmp, out)
}

// ProbeClean is Probe for files we upload: it also fails when ffprobe prints any complaint
// or the stream does not start at 0, both of which mean the file is malformed.
func ProbeClean(ctx context.Context, path string) (float64, error) {
	out, stderr, err := run(ctx, "ffprobe", "-v", "error", "-show_entries", "format=start_time,duration",
		"-of", "default=noprint_wrappers=1", path)
	if err != nil {
		return 0, err
	}
	if s := strings.TrimSpace(stderr); s != "" {
		return 0, fmt.Errorf("%s is malformed: %s", path, tail(s))
	}
	var start, dur float64
	var haveStart, haveDur bool
	for _, line := range strings.Split(out, "\n") {
		k, v, _ := strings.Cut(strings.TrimSpace(line), "=")
		f, perr := strconv.ParseFloat(v, 64)
		switch {
		case k == "start_time" && perr == nil:
			start, haveStart = f, true
		case k == "duration" && perr == nil:
			dur, haveDur = f, true
		}
	}
	if !haveStart || !haveDur || math.Abs(start) > 0.1 {
		return 0, fmt.Errorf("%s is malformed: start_time=%v duration=%v", path, start, dur)
	}
	return dur, nil
}

func SHA256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// CreationDate reads the container's creation_time tag as a local calendar date (YYYY-MM-DD).
func CreationDate(ctx context.Context, path string) (string, bool) {
	out, _, err := run(ctx, "ffprobe", "-v", "error", "-show_entries", "format_tags=creation_time",
		"-of", "default=noprint_wrappers=1:nokey=1", path)
	if err != nil {
		return "", false
	}
	return parseCreationDate(strings.TrimSpace(out), time.Now())
}

// parseCreationDate rejects the placeholder dates some muxers write (epoch 1904/1970) and future dates.
func parseCreationDate(s string, now time.Time) (string, bool) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || t.Year() < 2000 || t.After(now.Add(48*time.Hour)) {
		return "", false
	}
	return t.In(time.Local).Format("2006-01-02"), true
}
