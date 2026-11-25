package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"classrec/internal/desktop"
	"classrec/internal/detect"
	"classrec/internal/media"
	"classrec/internal/recorder"
)

// audibleDB is the mean volume above which a recording counts as having sound.
const audibleDB = -45

// runRecordTest records a short clip with the real OBS and checks it, because the fake OBS does
// not model the canvas, the encoder or audio. --kill-obs-at kills OBS mid-recording to check that
// the class continues in a second piece.
func runRecordTest(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("record-test", flag.ContinueOnError)
	app := fs.String("app", "meet", "")
	seconds := fs.Int("seconds", 30, "")
	killAt := fs.Int("kill-obs-at", 0, "")
	root := fs.String("root", defaultRoot, "")
	obsURL := fs.String("obs-url", "ws://localhost:4455", "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	target, err := testTarget(*app, desktop.Snapshot(time.Now()))
	if err != nil {
		return err
	}
	logf := func(format string, a ...any) { fmt.Fprintf(os.Stderr, format+"\n", a...) }
	logf("recording %q for %d s", target.Title, *seconds)
	rec, err := newRecorder(*root, *obsURL, logf)
	if err != nil {
		return err
	}
	if err := rec.Begin(ctx, target); err != nil {
		return err
	}
	for s := 1; s <= *seconds && ctx.Err() == nil; s++ {
		time.Sleep(time.Second)
		if s == *killAt {
			logf("killing obs64.exe")
			_ = exec.Command("taskkill", "/F", "/IM", "obs64.exe").Run() // a failed kill shows up as one piece
		}
		if s%5 == 0 {
			if err := rec.Tick(ctx); err != nil {
				logf("tick: %v", err)
			}
		}
	}
	class, err := rec.End(context.WithoutCancel(ctx))
	if err != nil {
		return err
	}
	fmt.Println(checkClass(context.WithoutCancel(ctx), class, rec.Problems(), *killAt > 0))
	return nil
}

// testTarget picks the window to capture. Another app's meeting means a class may be on, so it
// refuses; for Zoom the meeting is the point (zoom.us/test).
func testTarget(app string, sample detect.Sample) (recorder.Target, error) {
	var spec detect.AppSpec
	for _, s := range detect.Apps {
		if strings.EqualFold(string(s.App), app) {
			spec = s
		}
	}
	if spec.App == "" {
		return recorder.Target{}, fmt.Errorf("--app must be meet or zoom, not %q", app)
	}
	for other, title := range detect.Classify(sample).Meeting {
		if other != spec.App || other != detect.Zoom {
			return recorder.Target{}, fmt.Errorf("a class looks to be on (%s %q); not touching OBS", other, title)
		}
	}
	var best *detect.Window
	for i, w := range sample.Windows {
		if !strings.EqualFold(filepath.Base(w.Exe), spec.Exe) || w.Title == "" || !w.Visible || w.Minimized || w.Cloaked {
			continue
		}
		if best == nil || w.Foreground || spec.Matches(w) {
			best = &sample.Windows[i]
		}
	}
	if best == nil {
		return recorder.Target{}, fmt.Errorf("no open, visible %s window to record", spec.Exe)
	}
	return recorder.Target{App: spec.App, Title: best.Title, Class: best.Class}, nil
}

// checkClass probes every piece, extracts a frame from the first, and says PASS or FAIL per check.
func checkClass(ctx context.Context, class recorder.Class, problems []string, wantTwoPieces bool) string {
	var out strings.Builder
	verdict := func(pass bool, format string, a ...any) {
		fmt.Fprintf(&out, "%s  %s\n", map[bool]string{true: "PASS", false: "FAIL"}[pass], fmt.Sprintf(format, a...))
	}
	fmt.Fprintf(&out, "class folder: %s\n", class.Dir)
	for i, file := range class.Files {
		info, err := media.Describe(ctx, file)
		if err != nil {
			verdict(false, "%s: %v", filepath.Base(file), err)
			continue
		}
		describe(&out, file, info)
		mean, err := media.MeanVolume(ctx, file)
		verdict(err == nil && mean > audibleDB, "%s audio mean %.1f dB, want > %d (%v)", filepath.Base(file), mean, audibleDB, err)
		if i == 0 {
			png := filepath.Join(class.Dir, "frame.png")
			if err := media.Frame(ctx, file, png, info.Duration/2); err != nil {
				verdict(false, "frame: %v", err)
			} else {
				fmt.Fprintf(&out, "frame: %s\n", png)
			}
		}
	}
	if wantTwoPieces {
		starts := pieceStarts(class.Dir)
		verdict(len(class.Files) == 2 && len(starts) == 2, "two pieces in one folder: %d file(s), pieces.json starts %v", len(class.Files), starts)
	}
	verdict(len(problems) == 0, "recorder problems: %v", problems)
	return strings.TrimRight(out.String(), "\n")
}

func describe(out *strings.Builder, file string, info media.Info) {
	fmt.Fprintf(out, "%s: %.1f MB, %.1f s, %.0f kbps\n", filepath.Base(file), float64(info.SizeByte)/1e6, info.Duration, float64(info.BitRate)/1000)
	if v := info.Video; v != nil {
		fmt.Fprintf(out, "  video: %s %s %dx%d at %.2f fps\n", v.Codec, v.Profile, v.Width, v.Height, v.FPS)
	}
	for _, a := range info.Audio {
		fmt.Fprintf(out, "  audio: %s %d Hz, %d ch\n", a.Codec, a.SampleRate, a.Channels)
	}
}

func pieceStarts(dir string) []string {
	var info recorder.Pieces
	content, err := os.ReadFile(filepath.Join(dir, recorder.PiecesFile))
	if err != nil || json.Unmarshal(content, &info) != nil {
		return nil
	}
	var starts []string
	for _, p := range info.Pieces {
		starts = append(starts, p.Start.Format("15:04:05"))
	}
	return starts
}
