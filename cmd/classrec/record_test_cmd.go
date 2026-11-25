package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"classrec/internal/desktop"
	"classrec/internal/detect"
	"classrec/internal/media"
	"classrec/internal/recorder"
)

// runRecordTest records a short clip of the real OBS and says what came out, so the setup can be
// judged on the real thing: the fake OBS does not model the canvas.
func runRecordTest(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("record-test", flag.ContinueOnError)
	app := fs.String("app", "meet", "")
	seconds := fs.Int("seconds", 30, "")
	root := fs.String("root", defaultRoot, "")
	obsURL := fs.String("obs-url", "ws://localhost:4455", "")
	profile := fs.String("profile", recorder.DefaultProfileName, "") // another name tests first-run setup on a fresh profile
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *seconds < 5 {
		return errors.New("--seconds must be at least 5")
	}

	sample := desktop.Snapshot(time.Now())
	if observed := detect.Classify(sample); observed.ZoomWindow != "" || observed.MeetWindow != "" {
		return fmt.Errorf("a class looks to be in progress (%q%q); not touching OBS", observed.ZoomWindow, observed.MeetWindow)
	}
	target, err := testTarget(*app, sample)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "recording %q (%s) for %d s\n", target.Title, target.Exe, *seconds)

	logf := func(format string, a ...any) { fmt.Fprintf(os.Stderr, format+"\n", a...) }
	rec, err := newRecorder(*root, *obsURL, *profile, logf)
	if err != nil {
		return err
	}
	if err := rec.Begin(ctx, target); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Duration(*seconds) * time.Second):
	}
	filing, err := rec.End(context.WithoutCancel(ctx))
	if err != nil {
		return err
	}
	class := filing.Class()
	fmt.Println(describeClass(context.WithoutCancel(ctx), class, rec.Problems()))
	return nil
}

// testTarget picks the window to capture: Edge's, or Zoom's, whichever is open and on screen.
func testTarget(app string, sample detect.Sample) (recorder.Target, error) {
	var exe, name string
	switch strings.ToLower(app) {
	case "meet":
		exe, name = "msedge.exe", detect.Meet
	case "zoom":
		exe, name = "zoom.exe", detect.Zoom
	default:
		return recorder.Target{}, fmt.Errorf("--app must be meet or zoom, not %q", app)
	}
	var best *detect.Window
	for i, w := range sample.Windows {
		if !strings.EqualFold(exeName(w.Exe), exe) || w.Title == "" || !w.Visible || w.Minimized || w.Cloaked {
			continue
		}
		if best == nil || w.Foreground {
			best = &sample.Windows[i]
		}
	}
	if best == nil {
		return recorder.Target{}, fmt.Errorf("no open, visible %s window to record; open one and try again", exe)
	}
	return recorder.Target{App: name, Title: best.Title, Class: best.Class, Exe: exeName(best.Exe)}, nil
}

// describeClass probes the first piece and extracts a frame from its middle.
func describeClass(ctx context.Context, class recorder.Class, problems []string) string {
	var out strings.Builder
	fmt.Fprintf(&out, "class folder: %s\n", class.Dir)
	fmt.Fprintf(&out, "files: %d filed, %d left in staging\n", len(class.Files), class.Pending)
	if len(class.Files) == 0 {
		out.WriteString("nothing to probe\n")
	} else {
		first := class.Files[0]
		info, err := media.Describe(ctx, first)
		if err != nil {
			fmt.Fprintf(&out, "ffprobe failed: %v\n", err)
		} else {
			fmt.Fprintf(&out, "file: %s (%.1f MB, %.1f s)\n", filepath.Base(first), float64(info.SizeByte)/1e6, info.Duration)
			if v := info.Video; v != nil {
				fmt.Fprintf(&out, "video: %s %s %dx%d at %.2f fps\n", v.Codec, v.Profile, v.Width, v.Height, v.FPS)
			} else {
				out.WriteString("video: NONE\n")
			}
			fmt.Fprintf(&out, "bitrate (whole file): %.0f kbps\n", float64(info.BitRate)/1000)
			if len(info.Audio) == 0 {
				out.WriteString("audio: NONE\n")
			}
			for _, a := range info.Audio {
				fmt.Fprintf(&out, "audio: %s %d Hz, %d ch\n", a.Codec, a.SampleRate, a.Channels)
			}
			png := filepath.Join(class.Dir, "frame.png")
			if err := media.Frame(ctx, first, png, info.Duration/2); err != nil {
				fmt.Fprintf(&out, "frame: failed: %v\n", err)
			} else {
				fmt.Fprintf(&out, "frame: %s\n", png)
			}
		}
	}
	if len(problems) == 0 {
		out.WriteString("recorder problems: none\n")
	}
	for _, p := range problems {
		fmt.Fprintf(&out, "recorder problem: %s\n", p)
	}
	return strings.TrimRight(out.String(), "\n")
}
