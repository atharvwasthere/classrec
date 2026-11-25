package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"classrec/internal/desktop"
	"classrec/internal/detect"
	"classrec/internal/obs"
	"classrec/internal/recorder"
)

const defaultRoot = `C:\Users\Admin\Class Recordings`

type recordOptions struct {
	Mode     modeChoice
	Interval time.Duration
	Root     string
	OBSURL   string
}

// runRecordLoop watches for classes and records them with OBS until ctx ends.
func runRecordLoop(ctx context.Context, opts recordOptions) error {
	logf, closeLog, err := openAgentLog()
	if err != nil {
		return err
	}
	defer closeLog()

	rec, err := newRecorder(opts.Root, opts.OBSURL, logf)
	if err != nil {
		return err
	}
	logf("agent started: mode %s (%s), recordings in %s", opts.Mode.Mode, opts.Mode.Source, opts.Root)

	if moved, still := rec.RetryPending(ctx); moved+still > 0 {
		logf("files left in staging by an earlier run: %d filed, %d still stuck", moved, still)
	}

	machine := detect.Machine{Cfg: detect.DefaultConfig}
	tick := time.NewTicker(opts.Interval)
	defer tick.Stop()
	for {
		step(ctx, &machine, rec, opts, logf)
		select {
		case <-ctx.Done():
			finishOnExit(rec, opts, logf)
			return nil
		case <-tick.C:
		}
	}
}

// step takes one look at the desktop and acts on it.
func step(ctx context.Context, machine *detect.Machine, rec *recorder.Recorder, opts recordOptions, logf func(string, ...any)) {
	sample := desktop.Snapshot(time.Now())
	if rec.Recording() {
		sample.AppAudioLive = rec.AppAudioLive()
	}
	event := machine.Step(sample.Time, detect.Classify(sample))

	switch {
	case event != nil && event.Kind == detect.Stop:
		endClass(ctx, rec, opts, logf)
	case event != nil && event.Kind == detect.Start:
		logf("class detected: %s %q", event.App, event.Title)
		beginClass(ctx, rec, targetFor(*event, sample), logf)
	case rec.Recording():
		if err := rec.Tick(ctx); err != nil {
			logf("could not keep the recording going: %v", err)
		}
	case machine.Phase == detect.Recording || machine.Phase == detect.Ending:
		// the class is on but a start failed earlier: try again
		beginClass(ctx, rec, targetFor(detect.Event{App: machine.App, Title: machine.Title}, sample), logf)
	}
}

func beginClass(ctx context.Context, rec *recorder.Recorder, target recorder.Target, logf func(string, ...any)) {
	if err := rec.Begin(ctx, target); err != nil {
		logf("could not start recording: %v", err)
		return
	}
	logf("recording started")
}

func endClass(ctx context.Context, rec *recorder.Recorder, opts recordOptions, logf func(string, ...any)) {
	if !rec.Recording() {
		return
	}
	filing, err := rec.End(ctx)
	if err != nil {
		logf("problem stopping the recording: %v", err)
	}
	if filing == nil || filing.Dir == "" {
		return
	}
	logf("class recording ended; filing it into %s", filing.Dir)
	go func() {
		class := filing.Class() // returns when every piece is filed or parked in staging
		logf("class filed in %s: %d file(s), %d left in staging", class.Dir, len(class.Files), class.Pending)
		if opts.Mode.Mode == modeFull {
			logf("mode is full, but transcribing after a class is not wired in yet; nothing was sent to Sarvam")
		}
	}()
}

// finishOnExit stops a recording in progress when the agent is told to quit, so the file is closed
// properly, then gives filing a minute to finish. Anything not moved by then is retried at the next start.
func finishOnExit(rec *recorder.Recorder, opts recordOptions, logf func(string, ...any)) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if rec.Recording() {
		logf("agent stopping during a class; stopping the recording")
		endClass(ctx, rec, opts, logf)
	}
	waitCtx, stopWaiting := context.WithTimeout(ctx, time.Minute)
	defer stopWaiting()
	rec.Wait(waitCtx)
}

// newRecorder builds the recorder the agent and record-test share: OBS at url, with its password
// from the .obs-password file next to classrec.
func newRecorder(root, url string, logf func(string, ...any)) (*recorder.Recorder, error) {
	password, err := readOBSPassword()
	if err != nil {
		return nil, err
	}
	return recorder.New(recorder.Config{
		Root:       root,
		ProfileDir: obsProfileDir(recorder.DefaultProfileName),
		Dial: func(ctx context.Context) (recorder.OBS, error) {
			client, err := obs.Connect(ctx, url, password)
			if err != nil {
				return nil, err
			}
			return client, nil
		},
		OBSRunning: recorder.OBSRunning,
		LaunchOBS:  recorder.LauncherFor(recorder.DefaultOBSBin),
		Log:        logf,
	}), nil
}

// targetFor finds the meeting window the event named, to tell OBS which window to capture.
func targetFor(event detect.Event, sample detect.Sample) recorder.Target {
	target := recorder.Target{App: event.App, Title: event.Title}
	for _, w := range sample.Windows {
		if w.Title == event.Title {
			target.Class = w.Class
			target.Exe = exeName(w.Exe)
			break
		}
	}
	return target
}

func exeName(path string) string {
	return path[strings.LastIndexAny(path, `\/`)+1:]
}

func readOBSPassword() (string, error) {
	path := filepath.Join(exeDir(), ".obs-password")
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("the obs websocket password goes in %s: %w", path, err)
	}
	return strings.TrimSpace(string(content)), nil
}

// obsProfileDir is where OBS keeps a profile's files.
func obsProfileDir(name string) string {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		return ""
	}
	return filepath.Join(appData, "obs-studio", "basic", "profiles", name)
}

// openAgentLog returns a logger that writes to stderr and to logs\agent-<date>.log next to classrec.
func openAgentLog() (logf func(string, ...any), closeLog func(), err error) {
	dir := filepath.Join(exeDir(), "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "agent-"+time.Now().Format("2006-01-02")+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, nil, err
	}
	logf = func(format string, a ...any) {
		line := time.Now().Format("2006-01-02 15:04:05 ") + fmt.Sprintf(format, a...) + "\n"
		fmt.Fprint(os.Stderr, line)
		f.WriteString(line)
	}
	return logf, func() { f.Close() }, nil
}
