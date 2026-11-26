package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"classrec/internal/desktop"
	"classrec/internal/detect"
	"classrec/internal/marks"
	"classrec/internal/media"
	"classrec/internal/overlay"
	"classrec/internal/recorder"
)

const minFreeBytes = 20 << 30

// check is one selftest line.
type check struct {
	name string
	run  func(ctx context.Context) (detail string, err error)
}

// runSelftest checks everything the agent relies on, without calling Sarvam, and writes
// logs\selftest-<time>.txt. It fails if any check fails.
func runSelftest(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("selftest", flag.ContinueOnError)
	root := fs.String("root", defaultRoot, "")
	obsURL := fs.String("obs-url", "ws://localhost:4455", "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	checks := []check{
		{"DPI aware", func(context.Context) (string, error) {
			return "", boolErr(overlay.DPIAwareness(), "the manifest did not take effect")
		}},
		{"overlay never takes focus", checkOverlayFocus},
		{"mic record readable", func(context.Context) (string, error) { return "", desktop.MicReadable() }},
		{"obs records video and audio", func(ctx context.Context) (string, error) { return checkOBS(ctx, *root, *obsURL) }},
		{"keep-awake", checkKeepAwake},
		{"scheduled task runs as " + currentUser(), checkTask},
		{"mode", checkMode},
		{"disk free over 20 GB", func(context.Context) (string, error) { return checkDisk(*root) }},
	}
	var report strings.Builder
	fmt.Fprintf(&report, "classrec %s selftest, %s\n", version, time.Now().Format("2006-01-02 15:04:05"))
	failed := 0
	for _, c := range checks {
		detail, err := c.run(ctx)
		verdict := "PASS"
		if err != nil {
			verdict, failed, detail = "FAIL", failed+1, strings.TrimSpace(detail+" "+err.Error())
		}
		fmt.Fprintf(&report, "%s  %s  %s\n", verdict, c.name, detail)
	}
	fmt.Print(report.String())
	path := filepath.Join(exeDir(), "logs", "selftest-"+time.Now().Format("2006-01-02_150405")+".txt")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
		_ = os.WriteFile(path, []byte(report.String()), 0o644) // the printed report stands if this fails
		copyToReports(*root, path, "")
	}
	if failed > 0 {
		return fmt.Errorf("%d check(s) failed", failed)
	}
	return nil
}

func boolErr(ok bool, why string) error {
	if ok {
		return nil
	}
	return errors.New(why)
}

// checkOverlayFocus shows the overlay, clicks its button like a person would, and checks that
// the foreground window did not change and the click was saved as a mark.
func checkOverlayFocus(context.Context) (string, error) {
	marksPath := filepath.Join(os.TempDir(), "classrec-selftest-marks.json")
	_ = os.Remove(marksPath) // the selftest's own scratch file
	defer os.Remove(marksPath)
	o, err := overlay.New(overlay.Config{OnMark: func() error { return marks.AppendTime(marksPath, time.Now()) }})
	if err != nil {
		return "", err
	}
	defer o.Close()
	o.Show()
	time.Sleep(700 * time.Millisecond)
	x, y := o.ButtonCenter()
	before, after := clickAt(x, y)
	times, _ := marks.ReadTimes(marksPath)
	if before != after {
		return "", errors.New("the click moved the focus")
	}
	return fmt.Sprintf("%d mark saved", len(times)), boolErr(len(times) == 1, "the click was not saved")
}

// checkOBS records 10 s into a scratch folder and checks the file has video and audio. It
// refuses while a class is on, so it never takes OBS from a real class.
func checkOBS(ctx context.Context, root, url string) (string, error) {
	if observed := detect.Classify(desktop.Snapshot(time.Now())); observed.InClass() {
		return "", errors.New("skipped: a class is on")
	}
	scratch, err := os.MkdirTemp("", "classrec-selftest-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(scratch) // the selftest's own 10 s clip, never a class
	rec, err := newRecorder(scratch, url, func(string, ...any) {})
	if err != nil {
		return "", err
	}
	if err := rec.Begin(ctx, recorder.Target{App: detect.Meet, Title: "classrec selftest"}); err != nil {
		return "", err
	}
	time.Sleep(10 * time.Second)
	class, err := rec.End(ctx)
	if err != nil || len(class.Files) == 0 {
		return "", fmt.Errorf("no recording: %v", err)
	}
	info, err := media.Describe(ctx, class.Files[0])
	if err != nil {
		return "", err
	}
	if info.Video == nil || len(info.Audio) == 0 {
		return "", fmt.Errorf("video %v, %d audio stream(s)", info.Video != nil, len(info.Audio))
	}
	detail := fmt.Sprintf("%dx%d %s at %.0f fps, %.0f kbps", info.Video.Width, info.Video.Height, info.Video.Codec, info.Video.FPS, float64(info.BitRate)/1000)
	if p := rec.Problems(); len(p) > 0 {
		return detail, fmt.Errorf("settings differ from install: %s", strings.Join(p, "; "))
	}
	return detail, nil
}

func checkKeepAwake(context.Context) (string, error) {
	ok := desktop.KeepAwake(true)
	desktop.KeepAwake(false)
	return "", boolErr(ok, "Windows refused to stay awake")
}

func checkTask(ctx context.Context) (string, error) {
	owner, err := taskRunsAs(ctx)
	if err != nil {
		return "", err
	}
	if !isCurrentUser(owner) {
		return owner, errors.New("the task runs as someone else")
	}
	return owner, nil
}

// isCurrentUser reports whether a task's UserId is the current user. Task Scheduler reports it as
// a SID (the Uid of os/user on Windows), as DOMAIN\name, or as the bare name.
func isCurrentUser(id string) bool {
	if me, err := user.Current(); err == nil && strings.EqualFold(id, me.Uid) {
		return true
	}
	name := currentUser()
	return strings.EqualFold(id, name) || strings.HasSuffix(strings.ToLower(name), `\`+strings.ToLower(id))
}

func checkMode(context.Context) (string, error) {
	m, err := agentMode("", settingsPath())
	return fmt.Sprintf("%s (%s)", m.Mode, m.Source), err
}

func checkDisk(root string) (string, error) {
	dir := root
	if _, err := os.Stat(dir); err != nil {
		dir = filepath.VolumeName(root) + `\`
	}
	free, err := desktop.FreeBytes(dir)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%.0f GB", float64(free)/(1<<30)), boolErr(free > minFreeBytes, "not enough space for classes")
}
