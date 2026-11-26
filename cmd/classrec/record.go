package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"classrec/internal/desktop"
	"classrec/internal/detect"
	"classrec/internal/marks"
	"classrec/internal/obs"
	"classrec/internal/overlay"
	"classrec/internal/recorder"
)

const defaultRoot = `C:\Users\Admin\Class Recordings`

type recordOptions struct {
	Mode     modeChoice
	Interval time.Duration
	Root     string
	OBSURL   string
}

// agent is the record loop's state: it watches for classes and records them with OBS.
type agent struct {
	opts    recordOptions
	rec     *recorder.Recorder
	machine *detect.Machine
	log     *agentLog
	pill    *overlay.Overlay // nil if the overlay could not start
	warn    *warnings        // nil between classes
	level   overlay.Level
}

// runRecordLoop records classes until ctx ends.
func runRecordLoop(ctx context.Context, opts recordOptions) error {
	// Keep-awake is a per-thread setting in Windows, so this loop keeps one thread.
	runtime.LockOSThread()
	if err := desktop.SingleInstance("classrec-agent", 30*time.Second); err != nil {
		return err
	}
	log, err := openAgentLog()
	if err != nil {
		return err
	}
	defer log.close()
	rec, err := newRecorder(opts.Root, opts.OBSURL, log.printf)
	if err != nil {
		return err
	}
	a := &agent{opts: opts, rec: rec, machine: detect.NewMachine(detect.DefaultConfig), log: log}
	a.pill, err = overlay.New(overlay.Config{SettingsPath: settingsPath(), OnMark: a.mark})
	if err != nil {
		log.printf("no overlay: %v", err)
	}
	log.printf("agent started: mode %s (%s), recordings in %s, silence below %.0f dB", opts.Mode.Mode, opts.Mode.Source, opts.Root, silenceDB())
	if moved, still := rec.RetryPending(ctx); moved+still > 0 {
		log.printf("files left in staging by an earlier run: %d filed, %d still stuck", moved, still)
	}

	updates, err := newUpdater(log.printf)
	if err != nil {
		return err
	}
	lastUpdateCheck := time.Now().Add(-updateEvery + 10*time.Minute) // first look 10 min after start
	tick := time.NewTicker(opts.Interval)
	defer tick.Stop()
	for {
		a.step(ctx)
		if a.machine.Phase() == detect.Idle && time.Since(lastUpdateCheck) >= updateEvery {
			lastUpdateCheck = time.Now()
			if updated, err := updates.update(ctx); err != nil {
				log.printf("update check: %v", err)
			} else if updated {
				return errUpdated
			}
		}
		select {
		case <-ctx.Done():
			if rec.Recording() {
				log.printf("agent stopping during a class; stopping the recording")
				stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				a.endClass(stopCtx)
				cancel()
			}
			return nil
		case <-tick.C:
		}
	}
}

// step takes one look at the desktop and acts on it.
func (a *agent) step(ctx context.Context) {
	sample := desktop.Snapshot(time.Now())
	if a.rec.Recording() {
		sample.AppAudioLive = a.rec.AppAudioLive()
	}
	event := a.machine.Step(sample.Time, detect.Classify(sample))
	switch {
	case event != nil && event.Kind == detect.Stop:
		a.endClass(ctx)
	case event != nil && event.Kind == detect.Start:
		a.log.printf("class detected: %s %q", event.App, event.Title)
		a.beginClass(ctx, targetFor(event.App, event.Title, sample))
	case a.rec.Recording():
		if err := a.rec.Tick(ctx); err != nil {
			a.log.printf("could not keep the recording going: %v", err)
		}
		a.updatePill(ctx, sample)
	case a.machine.Phase() == detect.Recording || a.machine.Phase() == detect.Ending:
		app, title := a.machine.Class() // the class is on but a start failed earlier: try again
		a.beginClass(ctx, targetFor(app, title, sample))
	}
}

func (a *agent) beginClass(ctx context.Context, target recorder.Target) {
	a.log.startClass(classFile(a.opts.Root, "class.log"))
	parkLeftover(classFile(a.opts.Root, "marks.json"))
	if err := a.rec.Begin(ctx, target); err != nil {
		a.log.printf("could not start recording: %v", err)
		return
	}
	a.log.printf("recording started; mode %s", a.opts.Mode.Mode)
	desktop.KeepAwake(true)
	a.warn, a.level = newWarnings(time.Now()), overlay.Normal
	if a.pill != nil {
		a.pill.SetStatus(overlay.Normal, "")
		a.pill.Show()
	}
}

func (a *agent) endClass(ctx context.Context) {
	if !a.rec.Recording() {
		return
	}
	if a.pill != nil {
		a.pill.Hide()
	}
	desktop.KeepAwake(false)
	a.warn = nil
	class, err := a.rec.End(ctx)
	if err != nil {
		a.log.printf("problem ending the class: %v", err)
	}
	a.log.printf("class filed in %s: %d file(s), %d left in staging; mode %s", class.Dir, len(class.Files), class.Pending, a.opts.Mode.Mode)
	a.log.endClass()
	if class.Dir != "" {
		for _, name := range []string{"marks.json", "class.log"} {
			moveIfPresent(classFile(a.opts.Root, name), filepath.Join(class.Dir, name), a.log.printf)
		}
	}
	afterClass(ctx, a.opts.Mode.Mode, class, a.log.printf, processClass)
}

// updatePill shows what needs her attention, and logs each change.
func (a *agent) updatePill(ctx context.Context, sample detect.Sample) {
	if a.warn == nil {
		return
	}
	skipped, _ := a.rec.SkippedFrames(ctx) // unknown counts as none; the recorder logs connection trouble
	level, text, dropped := a.warn.update(classSignals{
		now: sample.Time, audible: sample.AppAudioLive, onBattery: desktop.OnBattery(), skippedFrames: skipped,
	})
	if dropped {
		a.log.printf("obs dropped frames: %d so far in this piece", skipped)
	}
	if level != a.level {
		a.log.printf("pill: %v %s", level, text)
		a.level = level
	}
	if a.pill != nil {
		a.pill.SetStatus(level, text)
	}
}

// mark records "I didn't get this" at the current moment. It runs on the overlay's thread.
func (a *agent) mark() error {
	return marks.AppendTime(classFile(a.opts.Root, "marks.json"), time.Now())
}

// classFile is where a file of the class in progress lives until the class is filed.
func classFile(root, name string) string { return filepath.Join(root, "_staging", name) }

// parkLeftover renames a file an interrupted class left behind, so the new class starts clean.
func parkLeftover(path string) {
	if _, err := os.Stat(path); err == nil {
		ext := filepath.Ext(path)
		_ = os.Rename(path, strings.TrimSuffix(path, ext)+time.Now().Format(" 2006-01-02 15-04-05")+ext)
	}
}

// moveIfPresent moves src to dst unless dst exists; it never overwrites.
func moveIfPresent(src, dst string, logf func(string, ...any)) {
	if _, err := os.Stat(src); err != nil {
		return
	}
	if _, err := os.Stat(dst); err == nil {
		logf("not moving %s: %s exists", src, dst)
		return
	}
	if err := os.Rename(src, dst); err != nil {
		logf("could not move %s: %v", src, err)
	}
}

// newRecorder builds the recorder the agent, record-test and selftest share: OBS at url, with
// its password from the .obs-password file next to classrec.
func newRecorder(root, url string, logf func(string, ...any)) (*recorder.Recorder, error) {
	password, err := readOBSPassword()
	if err != nil {
		return nil, err
	}
	return recorder.New(recorder.Config{
		Root:       root,
		ProfileDir: filepath.Join(obsProfilesDir(), obs.ProfileFolder(recorder.ProfileName)),
		Dial:       func(ctx context.Context) (*obs.Client, error) { return obs.Connect(ctx, url, password) },
		OBSRunning: recorder.OBSRunning,
		LaunchOBS:  recorder.LauncherFor(recorder.DefaultOBSBin),
		SilenceDB:  silenceDB(),
		Log:        logf,
	}), nil
}

// targetFor finds the named meeting window, to tell OBS which window to capture.
func targetFor(app detect.App, title string, sample detect.Sample) recorder.Target {
	target := recorder.Target{App: app, Title: title}
	for _, w := range sample.Windows {
		if w.Title == title {
			target.Class = w.Class
			break
		}
	}
	return target
}

func readOBSPassword() (string, error) {
	path := filepath.Join(exeDir(), ".obs-password")
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("the obs websocket password goes in %s: %w", path, err)
	}
	return strings.TrimSpace(string(content)), nil
}

// obsProfilesDir is where OBS keeps its profiles.
func obsProfilesDir() string {
	return filepath.Join(os.Getenv("APPDATA"), "obs-studio", "basic", "profiles")
}
