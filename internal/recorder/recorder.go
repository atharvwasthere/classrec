// Package recorder runs OBS for the agent: it sets OBS up the same way every time, records one
// class, copes with OBS dying mid-class, and files the recording.
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

	"classrec/internal/obs"
)

type videoSettingsT = obs.VideoSettings

// OBS is the part of *obs.Client the recorder uses, so tests can run against the fake.
type OBS interface {
	StartRecord(ctx context.Context) error
	StopRecord(ctx context.Context) (string, error)
	RecordStatus(ctx context.Context) (obs.RecordStatus, error)
	Scenes(ctx context.Context) ([]string, error)
	Inputs(ctx context.Context) ([]string, error)
	CreateScene(ctx context.Context, name string) error
	CreateInput(ctx context.Context, scene, name, kind string, settings map[string]any) error
	SetInputSettings(ctx context.Context, name string, settings map[string]any) error
	SetCurrentScene(ctx context.Context, name string) error
	SetRecordDirectory(ctx context.Context, dir string) error
	RecordDirectory(ctx context.Context) (string, error)
	Profiles(ctx context.Context) ([]string, string, error)
	CreateProfile(ctx context.Context, name string) error
	SetCurrentProfile(ctx context.Context, name string) error
	SetProfileParameter(ctx context.Context, category, name, value string) error
	ProfileParameter(ctx context.Context, category, name string) (string, error)
	SetVideoSettings(ctx context.Context, v obs.VideoSettings) error
	VideoSettings(ctx context.Context) (obs.VideoSettings, error)
	SetInputMute(ctx context.Context, name string, muted bool) error
	GlobalAudioInputs(ctx context.Context) ([]string, error)
	InputLevel(name string) (obs.InputLevel, bool)
	Err() error
	Close() error
}

// Config says where things are and how to reach OBS. Dial, OBSRunning and LaunchOBS are required.
type Config struct {
	Root        string // where finished classes go, e.g. C:\Users\Admin\Class Recordings
	ProfileName string // default "classrec"
	ProfileDir  string // OBS's folder for that profile, where recordEncoder.json lives; "" skips that file
	Encoder     string // default DefaultEncoder

	Dial       func(ctx context.Context) (OBS, error)
	OBSRunning func() bool
	LaunchOBS  func() error

	SilenceDB       float64       // app audio at or below this is silence; default -55 dBFS
	AudioFresh      time.Duration // a level older than this is not a level; default 5 s
	ConnectAttempts int           // one try a second after OBS starts; default 60
	Move            MoveOptions

	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error
	Log   func(format string, a ...any)
}

// Target is the class app and its meeting window, as the detector saw it.
type Target struct {
	App   string // detect.Zoom or detect.Meet
	Title string
	Class string
	Exe   string // file name of the app's exe
}

// Class is one recorded class once filed.
type Class struct {
	Dir     string   // folder in Root, empty if nothing was recorded
	Files   []string // pieces in recording order, as filed
	Pending int      // pieces still in staging because they could not be moved
}

// Recorder records one class at a time. Its methods are safe from any goroutine.
type Recorder struct {
	cfg Config

	mu       sync.Mutex
	client   OBS
	problems []string // settings OBS did not take, from the last ApplyProfile

	active    bool
	target    Target
	began     time.Time
	pieces    []string        // finished recording files, in order
	seen      map[string]bool // staging files that are not part of this class, or already counted
	restarts  int
	audioName string // OBS input whose level shows the class is audible
}

// New returns a recorder for cfg, with defaults filled in.
func New(cfg Config) *Recorder {
	if cfg.ProfileName == "" {
		cfg.ProfileName = DefaultProfileName
	}
	if cfg.Encoder == "" {
		cfg.Encoder = DefaultEncoder
	}
	if cfg.SilenceDB == 0 {
		cfg.SilenceDB = -55
	}
	if cfg.AudioFresh == 0 {
		cfg.AudioFresh = 5 * time.Second
	}
	if cfg.ConnectAttempts == 0 {
		cfg.ConnectAttempts = 60
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
	return &Recorder{cfg: cfg, seen: map[string]bool{}}
}

// Staging is where OBS writes while recording.
func (r *Recorder) Staging() string { return filepath.Join(r.cfg.Root, "_staging") }

// Problems lists the settings OBS did not take the last time the profile was applied.
func (r *Recorder) Problems() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.problems...)
}

// Recording reports whether a class is being recorded.
func (r *Recorder) Recording() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active
}

// Begin sets OBS up for the target and starts recording. A second Begin while a class is in
// progress is an error.
func (r *Recorder) Begin(ctx context.Context, t Target) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active {
		return errors.New("a class is already being recorded")
	}
	if err := r.connect(ctx); err != nil {
		return err
	}
	r.target = t
	r.began = r.cfg.Now()
	r.pieces = nil
	r.restarts = 0
	r.seen = map[string]bool{}
	if err := r.startPiece(ctx); err != nil {
		return err
	}
	r.active = true
	return nil
}

// Tick checks that OBS is still recording and puts it right if not: it reconnects after a dropped
// connection, and if OBS is gone it starts OBS again and records a new piece of the same class.
func (r *Recorder) Tick(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.active {
		return nil
	}
	if r.client != nil && r.client.Err() == nil && r.cfg.OBSRunning() {
		status, err := r.client.RecordStatus(ctx)
		if err == nil && status.Active {
			return nil
		}
	}
	return r.recover(ctx)
}

// recover runs when OBS stopped recording without being asked: it died, lost its connection, or
// someone stopped the recording by hand. The piece it was writing is kept, and a new one starts.
func (r *Recorder) recover(ctx context.Context) error {
	r.cfg.Log("recording stopped unexpectedly; recovering")
	r.dropClient()
	if err := r.connect(ctx); err != nil {
		return err
	}
	status, err := r.client.RecordStatus(ctx)
	if err == nil && status.Active {
		r.cfg.Log("obs is still recording; only the connection was lost")
		return nil
	}
	r.pieces = append(r.pieces, r.unclaimedFiles()...)
	r.restarts++
	return r.startPiece(ctx)
}

// End stops recording and files the pieces. It returns even if OBS is gone, with whatever the
// recording left in staging.
func (r *Recorder) End(ctx context.Context) (Class, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.active {
		return Class{}, errors.New("no class is being recorded")
	}
	r.active = false

	var stopErr error
	gotPath := false
	if r.client != nil && r.client.Err() == nil {
		path, err := r.client.StopRecord(ctx)
		var refused *obs.RequestError
		switch {
		case err == nil && path != "":
			r.pieces = append(r.pieces, path)
			r.seen[path] = true
			gotPath = true
		case errors.As(err, &refused) && refused.Code == codeOutputNotRunning:
			// someone already stopped it; the file is in staging
		case err != nil:
			stopErr = err
		}
	}
	if !gotPath {
		// OBS gave no path (it is gone, or was stopped by hand): take what it left in staging
		r.pieces = append(r.pieces, r.unclaimedFiles()...)
	}
	class := r.file(ctx)
	if stopErr != nil {
		// OBS died or hung up as we stopped. What it wrote is filed all the same.
		r.cfg.Log("stopping the recording: %v", stopErr)
		if len(r.pieces) == 0 {
			return class, fmt.Errorf("stopping the recording: %w", stopErr)
		}
	}
	return class, nil
}

// file moves the pieces into the class folder. Pieces that cannot be moved stay in staging and are
// remembered for RetryPending.
func (r *Recorder) file(ctx context.Context) Class {
	var class Class
	if len(r.pieces) == 0 {
		return class
	}
	day := r.began.Format("2006-01-02")
	class.Dir = filepath.Join(r.cfg.Root, day, fmt.Sprintf("%s %s", r.began.Format("2006-01-02 15-04-05"), r.target.App))
	for i, src := range r.pieces {
		dst := filepath.Join(class.Dir, fmt.Sprintf("part-%02d%s", i+1, filepath.Ext(src)))
		moved, err := moveFile(ctx, src, dst, r.moveOptions())
		if err != nil {
			r.cfg.Log("leaving %s in staging: %v", src, err)
			if perr := addPending(r.Staging(), pendingMove{Src: src, Dst: dst}); perr != nil {
				r.cfg.Log("could not note the pending move: %v", perr)
			}
			class.Pending++
			continue
		}
		class.Files = append(class.Files, moved)
	}
	return class
}

// RetryPending tries again to move files an earlier run left in staging. Call it when the agent starts.
func (r *Recorder) RetryPending(ctx context.Context) (moved, still int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var remaining []pendingMove
	for _, p := range readPending(r.Staging()) {
		if _, err := os.Stat(p.Src); err != nil {
			continue // already filed or removed by hand; nothing to retry
		}
		if _, err := moveFile(ctx, p.Src, p.Dst, r.moveOptions()); err != nil {
			r.cfg.Log("still cannot move %s: %v", p.Src, err)
			remaining = append(remaining, p)
			continue
		}
		moved++
	}
	if err := writePending(r.Staging(), remaining); err != nil {
		r.cfg.Log("could not update the pending list: %v", err)
	}
	return moved, len(remaining)
}

func (r *Recorder) moveOptions() MoveOptions {
	opts := r.cfg.Move
	if opts.sleep == nil {
		opts.sleep = r.cfg.Sleep
	}
	return opts
}

// AppAudioLive reports whether OBS currently hears sound from the class app. The detector uses it
// as its third reason to keep a class going.
func (r *Recorder) AppAudioLive() bool {
	r.mu.Lock()
	client, name := r.client, r.audioName
	r.mu.Unlock()
	if client == nil || name == "" {
		return false
	}
	level, ok := client.InputLevel(name)
	if !ok || r.cfg.Now().Sub(level.At) > r.cfg.AudioFresh {
		return false
	}
	return level.DB > r.cfg.SilenceDB
}

// startPiece sets OBS up and starts a recording. The caller holds r.mu and has a connected client.
func (r *Recorder) startPiece(ctx context.Context) error {
	problems, err := r.ApplyProfile(ctx)
	if err != nil {
		return err
	}
	r.problems = problems
	for _, p := range problems {
		r.cfg.Log("obs setting: %s", p)
	}
	if err := r.setRecordDirectory(ctx); err != nil {
		return err
	}
	if err := r.setUpScene(ctx); err != nil {
		return err
	}
	if err := r.muteGlobalAudio(ctx); err != nil {
		return err
	}
	r.markExistingFilesSeen()
	if err := r.client.StartRecord(ctx); err != nil {
		return fmt.Errorf("starting the recording: %w", err)
	}
	status, err := r.client.RecordStatus(ctx)
	if err != nil || !status.Active {
		return fmt.Errorf("obs says it is not recording after StartRecord (status %+v, error %v)", status, err)
	}
	return nil
}

// setRecordDirectory points OBS at staging and checks that it took.
func (r *Recorder) setRecordDirectory(ctx context.Context) error {
	staging := r.Staging()
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return err
	}
	if err := r.client.SetRecordDirectory(ctx, staging); err != nil {
		return fmt.Errorf("setting the record folder: %w", err)
	}
	got, err := r.client.RecordDirectory(ctx)
	if err != nil {
		return err
	}
	if !samePath(got, staging) {
		return fmt.Errorf("obs records into %q, not %q", got, staging)
	}
	return nil
}

func samePath(a, b string) bool {
	clean := func(p string) string { return strings.ToLower(filepath.Clean(filepath.FromSlash(p))) }
	return clean(a) == clean(b)
}

// muteGlobalAudio silences Desktop Audio and Mic/Aux: they belong to every scene, so a scene
// cannot leave them out, and they would add room noise and echo to the class.
func (r *Recorder) muteGlobalAudio(ctx context.Context) error {
	names, err := r.client.GlobalAudioInputs(ctx)
	if err != nil {
		return fmt.Errorf("finding the global audio inputs: %w", err)
	}
	for _, name := range names {
		if err := r.client.SetInputMute(ctx, name, true); err != nil {
			return fmt.Errorf("muting %s: %w", name, err)
		}
	}
	return nil
}

// markExistingFilesSeen notes the recordings already in staging, so only files made from now on count as pieces.
func (r *Recorder) markExistingFilesSeen() {
	entries, _ := os.ReadDir(r.Staging())
	for _, e := range entries {
		if !e.IsDir() {
			r.seen[filepath.Join(r.Staging(), e.Name())] = true
		}
	}
}

// unclaimedFiles lists recordings in staging that appeared since this class began and were not
// handed back by StopRecord, oldest first. OBS leaves one behind when it dies.
func (r *Recorder) unclaimedFiles() []string {
	type found struct {
		path string
		mod  time.Time
	}
	var files []found
	entries, _ := os.ReadDir(r.Staging())
	for _, e := range entries {
		path := filepath.Join(r.Staging(), e.Name())
		if e.IsDir() || r.seen[path] || !isRecording(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, found{path, info.ModTime()})
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].mod.Equal(files[j].mod) {
			return files[i].path < files[j].path
		}
		return files[i].mod.Before(files[j].mod)
	})
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.path
		r.seen[f.path] = true
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

// connect makes r.client a live connection, starting OBS first if it is not running.
func (r *Recorder) connect(ctx context.Context) error {
	if r.client != nil && r.client.Err() == nil {
		return nil
	}
	r.dropClient()
	launched := false
	var lastErr error
	for attempt := 0; attempt < r.cfg.ConnectAttempts; attempt++ {
		if !launched && !r.cfg.OBSRunning() {
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

func (r *Recorder) dropClient() {
	if r.client != nil {
		r.client.Close()
		r.client = nil
	}
}
