// Package recorder runs OBS for the agent: it records one class at a time, copes with OBS dying
// mid-class, and files the recording. OBS's profile is written once by `classrec install`; at
// runtime the recorder only selects it, sets the record folder, scene and mutes, and logs drift.
package recorder

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"classrec/internal/detect"
	"classrec/internal/obs"
)

// ProfileName is the OBS profile classrec records with.
const ProfileName = "classrec"

const (
	connectAttempts = 60 // a second apart: OBS takes a while to start
	audioFresh      = 5 * time.Second
	moveTimeout     = 10 * time.Second // how long OBS may hold a file after StopRecord
)

// Config says where things are and how to reach OBS.
type Config struct {
	Root       string // where classes are filed, e.g. D:\Class Recordings
	ProfileDir string // the classrec profile's folder, to check recordEncoder.json; "" skips it

	Dial       func(ctx context.Context) (*obs.Client, error)
	OBSRunning func() bool
	LaunchOBS  func() error

	SilenceDB float64 // app audio at or below this is silence; default -50 dBFS

	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error
	Log   func(format string, a ...any)

	rename func(from, to string) error // os.Rename; tests replace it
}

// Target is the class app and its meeting window, as the detector saw it.
type Target struct {
	App   detect.App
	Title string
	Class string // window class; "" means the app's usual one
}

// Class is one class once filed.
type Class struct {
	Dir     string   // its folder in Root
	Files   []string // pieces in recording order, as filed
	Pending int      // pieces left in staging because OBS still held them
}

type piece struct {
	path  string
	start time.Time // wall clock, when OBS began writing it
}

// Recorder records one class at a time. The agent calls it from one goroutine; the mutex only
// keeps a stray caller safe.
type Recorder struct {
	cfg Config
	mu  sync.Mutex

	client    *obs.Client
	active    bool
	target    Target
	began     time.Time
	pieceFrom time.Time       // start of the piece being recorded
	pieces    []piece         // finished pieces, in order
	seen      map[string]bool // staging files that are not part of this class, or already counted
	audioName string          // the OBS input whose level shows the class is audible
	problems  []string
}

// New returns a recorder for cfg, with defaults filled in.
func New(cfg Config) *Recorder {
	if cfg.SilenceDB == 0 {
		cfg.SilenceDB = -50
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleepCtx
	}
	if cfg.Log == nil {
		cfg.Log = func(string, ...any) {}
	}
	if cfg.rename == nil {
		cfg.rename = os.Rename
	}
	return &Recorder{cfg: cfg, seen: map[string]bool{}}
}

// staging is where OBS writes while recording.
func (r *Recorder) staging() string { return filepath.Join(r.cfg.Root, "_staging") }

// Recording reports whether a class is being recorded.
func (r *Recorder) Recording() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active
}

// Problems lists what OBS had different from the classrec setup the last time a recording started.
func (r *Recorder) Problems() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.problems...)
}

// Begin sets OBS up for the target and starts recording. A setup step OBS refuses is a Problem,
// never a reason to skip the class; only StartRecord failing is an error.
func (r *Recorder) Begin(ctx context.Context, t Target) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active {
		return errors.New("a class is already being recorded")
	}
	if err := r.connect(ctx, true); err != nil {
		return err
	}
	r.target, r.began, r.pieces, r.seen = t, r.cfg.Now(), nil, map[string]bool{}
	if err := r.startPiece(ctx); err != nil {
		return err
	}
	r.active = true
	return nil
}

// Tick checks that OBS is still recording and puts it right if not: it reconnects after a dropped
// connection, and if OBS is gone it starts OBS and records a new piece of the same class.
func (r *Recorder) Tick(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.active {
		return nil
	}
	if r.alive() && r.cfg.OBSRunning() {
		if status, err := r.client.RecordStatus(ctx); err == nil && status.Active {
			return nil
		}
	}
	r.cfg.Log("recording stopped unexpectedly; recovering")
	r.drop()
	if err := r.connect(ctx, true); err != nil {
		return err
	}
	if status, err := r.client.RecordStatus(ctx); err == nil && status.Active {
		r.cfg.Log("obs is still recording; only the connection was lost")
		return nil
	}
	for _, path := range r.unclaimedFiles() {
		r.pieces = append(r.pieces, piece{path, r.pieceFrom})
	}
	return r.startPiece(ctx)
}

// leftoverTitle names the class folder of a recording an earlier run left going.
const leftoverTitle = "Recording left running"

// StopLeftover stops a recording that an earlier run of the agent left going when it was killed
// mid-class, and files it as a class of its own, so OBS does not record into staging for ever
// (seen 2026-11-27). It leaves alone a recording into any other folder: someone using OBS by hand.
// ok is false when there was nothing to stop.
func (r *Recorder) StopLeftover(ctx context.Context) (class Class, ok bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active || !r.cfg.OBSRunning() {
		return Class{}, false, nil
	}
	if err := r.connect(ctx, false); err != nil {
		return Class{}, false, err
	}
	status, err := r.client.RecordStatus(ctx)
	if err != nil || !status.Active {
		return Class{}, false, err
	}
	if dir, err := r.client.RecordDirectory(ctx); err != nil || !samePath(dir, r.staging()) {
		return Class{}, false, err // recording somewhere else: not ours
	}
	path, err := r.stop(ctx)
	if path == "" {
		return Class{}, false, err
	}
	r.began = r.cfg.Now().Add(-time.Duration(status.DurationMS) * time.Millisecond)
	r.target = Target{Title: leftoverTitle}
	r.pieces, r.seen = []piece{{path, r.began}}, map[string]bool{path: true}
	class, err = r.file(ctx)
	r.pieces = nil
	return class, true, err
}

// Follow points the capture at the class window's current title. Edge renames its window when
// Meet moves from the lobby into the call and when she switches tabs, and OBS finds an Edge
// window only by its exact title: without this, picture and sound go blank (seen 2026-11-27).
func (r *Recorder) Follow(ctx context.Context, title, class string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.active || title == "" || title == r.target.Title || !r.alive() {
		return nil
	}
	spec, ok := detect.SpecFor(r.target.App)
	if !ok {
		return fmt.Errorf("unknown app %q", r.target.App)
	}
	r.cfg.Log("the class window is now %q; capturing it", title)
	r.target.Title, r.target.Class = title, class
	var errs []error
	for _, in := range captureInputs(spec, r.target) {
		errs = append(errs, r.client.SetInputSettings(ctx, in.name, in.settings))
	}
	return errors.Join(errs...)
}

// End stops the recording and files the class. If the connection dropped it reconnects, without
// starting OBS, to stop the recording properly; only if OBS is really gone does it take the files
// it left in staging.
func (r *Recorder) End(ctx context.Context) (Class, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.active {
		return Class{}, errors.New("no class is being recorded")
	}
	r.active = false
	if !r.alive() && r.cfg.OBSRunning() {
		if err := r.connect(ctx, false); err != nil {
			r.cfg.Log("could not reconnect to stop the recording: %v", err)
		}
	}
	path, err := r.stop(ctx)
	switch {
	case path != "":
		r.pieces = append(r.pieces, piece{path, r.pieceFrom})
		r.seen[path] = true
	default:
		if err != nil {
			r.cfg.Log("stopping the recording: %v", err)
		}
		for _, p := range r.unclaimedFiles() {
			r.pieces = append(r.pieces, piece{p, r.pieceFrom})
		}
	}
	if len(r.pieces) == 0 {
		return Class{}, errors.New("obs left no recording behind")
	}
	return r.file(ctx)
}

// stop asks OBS for the file it was writing; "" if OBS is gone or was not recording.
func (r *Recorder) stop(ctx context.Context) (string, error) {
	if !r.alive() {
		return "", errors.New("obs is gone")
	}
	path, err := r.client.StopRecord(ctx)
	var refused *obs.RequestError
	if errors.As(err, &refused) && refused.Code == codeOutputNotRunning {
		return "", nil // someone stopped it already; the file is in staging
	}
	return path, err
}

// AppAudioLive reports whether OBS hears sound from the class app: the detector's third reason
// to keep a class going.
func (r *Recorder) AppAudioLive() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.alive() || r.audioName == "" {
		return false
	}
	level, ok := r.client.InputLevel(r.audioName)
	if !ok || r.cfg.Now().Sub(level.At) > audioFresh {
		return false
	}
	return level.DB > r.cfg.SilenceDB
}

// SkippedFrames is how many frames OBS has dropped from the recording so far in this piece.
func (r *Recorder) SkippedFrames(ctx context.Context) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.alive() {
		return 0, errors.New("obs is not connected")
	}
	stats, err := r.client.Stats(ctx)
	return stats.OutputSkippedFrames, err
}

// startPiece sets OBS up and starts a recording. Only StartRecord failing, or OBS not reporting
// a recording a few seconds later, is an error.
func (r *Recorder) startPiece(ctx context.Context) error {
	r.problems = r.setUp(ctx)
	for _, p := range r.problems {
		r.cfg.Log("obs setup problem, recording anyway: %s", p)
	}
	r.markStagingSeen()
	started := r.cfg.Now()
	if err := r.startRecord(ctx); err != nil {
		return err
	}
	// OBS starts its output a moment after it acknowledges StartRecord
	for attempt := 0; attempt < 10; attempt++ {
		if status, err := r.client.RecordStatus(ctx); err == nil && status.Active {
			r.pieceFrom = started
			return nil
		}
		if err := r.cfg.Sleep(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	}
	return errors.New("obs says it is not recording after StartRecord")
}

// startRecord asks OBS to record. An OBS that has just started accepts connections before it can
// record and answers "not ready" for a few seconds, so that answer is retried for up to 30 s.
func (r *Recorder) startRecord(ctx context.Context) error {
	for attempt := 0; ; attempt++ {
		err := r.client.StartRecord(ctx)
		var refused *obs.RequestError
		switch {
		case err == nil:
			return nil
		case errors.As(err, &refused) && refused.Code == codeOutputRunning:
			r.cfg.Log("obs was already recording; carrying on with that recording")
			return nil
		case errors.As(err, &refused) && refused.Code == codeNotReady && attempt < notReadyAttempts:
			if err := r.cfg.Sleep(ctx, time.Second); err != nil {
				return err
			}
		default:
			return fmt.Errorf("starting the recording: %w", err)
		}
	}
}

// markStagingSeen notes the files already in staging, so only files made from now on count as pieces.
func (r *Recorder) markStagingSeen() {
	entries, _ := os.ReadDir(r.staging())
	for _, e := range entries {
		r.seen[filepath.Join(r.staging(), e.Name())] = true
	}
}

// unclaimedFiles lists recordings in staging that appeared during this class and were not handed
// back by StopRecord, oldest first. OBS leaves one behind when it dies.
func (r *Recorder) unclaimedFiles() []string {
	entries, _ := os.ReadDir(r.staging())
	var files []os.DirEntry
	for _, e := range entries {
		path := filepath.Join(r.staging(), e.Name())
		if !e.IsDir() && !r.seen[path] && isRecording(e.Name()) {
			files = append(files, e)
		}
	}
	modTime := func(e os.DirEntry) time.Time {
		info, err := e.Info()
		if err != nil {
			return time.Time{}
		}
		return info.ModTime()
	}
	sort.SliceStable(files, func(i, j int) bool { return modTime(files[i]).Before(modTime(files[j])) })
	paths := make([]string, len(files))
	for i, e := range files {
		paths[i] = filepath.Join(r.staging(), e.Name())
		r.seen[paths[i]] = true
	}
	return paths
}

func isRecording(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mkv", ".mp4", ".mov", ".flv", ".ts":
		return true
	}
	return false
}

func (r *Recorder) alive() bool { return r.client != nil && r.client.Err() == nil }

// connect makes the client a live connection; with launch it first starts OBS if it is not running.
func (r *Recorder) connect(ctx context.Context, launch bool) error {
	if r.alive() {
		return nil
	}
	r.drop()
	attempts := connectAttempts
	if !launch {
		attempts = 5
	}
	launched := false
	var lastErr error
	for range attempts {
		// checked on every try: an OBS that was dying a moment ago is gone now, and every second
		// spent dialling it is a second of class not recorded
		if launch && !launched && !r.cfg.OBSRunning() {
			r.cfg.Log("obs is not running; starting it")
			if err := r.cfg.LaunchOBS(); err != nil {
				return fmt.Errorf("starting obs: %w", err)
			}
			launched = true
		}
		client, err := r.cfg.Dial(ctx)
		if err == nil {
			r.client = client
			return nil
		}
		if errors.Is(err, obs.ErrAuthFailed) {
			return err
		}
		lastErr = err
		if err := r.cfg.Sleep(ctx, time.Second); err != nil {
			return err
		}
	}
	return fmt.Errorf("could not connect to obs: %w", lastErr)
}

func (r *Recorder) drop() {
	if r.client != nil {
		r.client.Close()
		r.client = nil
	}
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
