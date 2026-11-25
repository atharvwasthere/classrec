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
	SceneItemID(ctx context.Context, scene, source string) (int, error)
	SetSceneItemTransform(ctx context.Context, scene string, itemID int, transform map[string]any) error
	SetRecordDirectory(ctx context.Context, dir string) error
	RecordDirectory(ctx context.Context) (string, error)
	Profiles(ctx context.Context) ([]string, string, error)
	CreateProfile(ctx context.Context, name string) error
	SetCurrentProfile(ctx context.Context, name string) error
	SetProfileParameter(ctx context.Context, category, name, value string) error
	ProfileParameter(ctx context.Context, category, name string) (string, error)
	Monitors(ctx context.Context) ([]obs.Monitor, error)
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

	Dial        func(ctx context.Context) (OBS, error)
	OBSRunning  func() bool
	LaunchOBS   func() error
	DisplaySize func() (w, h int) // the primary display in true pixels; nil or 0,0 means ask OBS

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

// Filing is a class being moved into its folder in the background.
type Filing struct {
	Dir   string // the class folder, which already exists
	done  chan struct{}
	class Class
}

// Done is closed when every piece is filed or has been left in staging for a later retry.
func (f *Filing) Done() <-chan struct{} { return f.done }

// Class waits for the filing to finish and returns what it did.
func (f *Filing) Class() Class {
	<-f.done
	return f.class
}

type piece struct {
	Path  string
	Start time.Time // wall clock, when OBS began writing it
}

// Recorder records one class at a time.
//
// Two locks keep it responsive. opMu makes Begin, Tick and End run one at a time, and is held
// while talking to OBS, which can take a while (reconnecting, relaunching OBS). mu guards the
// few fields other goroutines read, is held only for an instant, and never across I/O, so
// AppAudioLive and Recording answer immediately even mid-reconnect. Filing a class runs on its
// own goroutine holding neither, so the next class can begin while the last one is still moving.
type Recorder struct {
	cfg Config

	opMu sync.Mutex

	mu        sync.Mutex
	client    OBS
	active    bool
	audioName string // OBS input whose level shows the class is audible
	problems  []string

	// below: only used with opMu held
	target   Target
	began    time.Time
	curStart time.Time
	pieces   []piece         // finished recording files, in order
	seen     map[string]bool // staging files that are not part of this class, or already counted
	canvasW  int             // base canvas of the current profile
	canvasH  int

	filings sync.WaitGroup
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

// Problems lists the settings OBS did not take the last time a recording was set up.
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

func (r *Recorder) setActive(active bool) {
	r.mu.Lock()
	r.active = active
	r.mu.Unlock()
}

func (r *Recorder) currentClient() OBS {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.client
}

// Begin sets OBS up for the target and starts recording. A setting OBS refuses is logged and
// listed in Problems, never a reason not to record; only StartRecord failing is an error.
func (r *Recorder) Begin(ctx context.Context, t Target) error {
	r.opMu.Lock()
	defer r.opMu.Unlock()
	if r.Recording() {
		return errors.New("a class is already being recorded")
	}
	if err := r.connect(ctx, true, r.cfg.ConnectAttempts); err != nil {
		return err
	}
	r.target = t
	r.began = r.cfg.Now()
	r.pieces = nil
	r.seen = map[string]bool{}
	if err := r.startPiece(ctx); err != nil {
		return err
	}
	r.setActive(true)
	return nil
}

// Tick checks that OBS is still recording and puts it right if not: it reconnects after a dropped
// connection, and if OBS is gone it starts OBS again and records a new piece of the same class.
func (r *Recorder) Tick(ctx context.Context) error {
	r.opMu.Lock()
	defer r.opMu.Unlock()
	if !r.Recording() {
		return nil
	}
	if client := r.currentClient(); client != nil && client.Err() == nil && r.cfg.OBSRunning() {
		status, err := client.RecordStatus(ctx)
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
	if err := r.connect(ctx, true, r.cfg.ConnectAttempts); err != nil {
		return err
	}
	status, err := r.currentClient().RecordStatus(ctx)
	if err == nil && status.Active {
		r.cfg.Log("obs is still recording; only the connection was lost")
		return nil
	}
	for _, path := range r.unclaimedFiles() {
		r.pieces = append(r.pieces, piece{path, r.curStart})
	}
	return r.startPiece(ctx)
}

// End stops recording and starts filing the pieces in the background; it returns as soon as the
// class folder exists. If the connection to OBS dropped it reconnects, without launching OBS, to
// stop the recording properly; only if OBS is really gone does it take the files from staging.
func (r *Recorder) End(ctx context.Context) (*Filing, error) {
	r.opMu.Lock()
	defer r.opMu.Unlock()
	if !r.Recording() {
		return nil, errors.New("no class is being recorded")
	}

	client := r.currentClient()
	if (client == nil || client.Err() != nil) && r.cfg.OBSRunning() {
		r.dropClient()
		if err := r.connect(ctx, false, 5); err != nil {
			r.cfg.Log("could not reconnect to stop the recording: %v", err)
		}
		client = r.currentClient()
	}

	var stopErr error
	gotPath := false
	if client != nil && client.Err() == nil {
		path, err := client.StopRecord(ctx)
		var refused *obs.RequestError
		switch {
		case err == nil && path != "":
			r.pieces = append(r.pieces, piece{path, r.curStart})
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
		for _, path := range r.unclaimedFiles() {
			r.pieces = append(r.pieces, piece{path, r.curStart})
		}
	}
	r.setActive(false)

	if stopErr != nil {
		// OBS died or hung up as we stopped. What it wrote is filed all the same.
		r.cfg.Log("stopping the recording: %v", stopErr)
	}
	if len(r.pieces) == 0 {
		done := make(chan struct{})
		close(done)
		err := errors.New("obs left no recording behind")
		if stopErr != nil {
			err = fmt.Errorf("stopping the recording: %w", stopErr)
		}
		return &Filing{done: done}, err
	}
	return r.startFiling(ctx, r.target, r.began, append([]piece(nil), r.pieces...))
}

// Wait blocks until every class being filed in the background is done, or ctx ends.
func (r *Recorder) Wait(ctx context.Context) {
	finished := make(chan struct{})
	go func() { r.filings.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-ctx.Done():
	}
}

// RetryPending tries again to move files an earlier run left in staging. Call it when the agent starts.
func (r *Recorder) RetryPending(ctx context.Context) (moved, still int) {
	var finished []pendingMove
	for _, p := range readPending(r.Staging()) {
		if _, err := os.Stat(p.Src); err != nil {
			finished = append(finished, p) // already filed or removed by hand; nothing to retry
			continue
		}
		if _, err := moveFile(ctx, p.Src, p.Dst, r.moveOptions()); err != nil {
			r.cfg.Log("still cannot move %s: %v", p.Src, err)
			still++
			continue
		}
		finished = append(finished, p)
		moved++
	}
	if err := removePending(r.Staging(), finished); err != nil {
		r.cfg.Log("could not update the pending list: %v", err)
	}
	return moved, still
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

// startPiece sets OBS up and starts a recording. Every setup step may fail without stopping the
// recording: a class recorded with the wrong scene or an unmuted mic is worth more than none.
// Only StartRecord failing, or OBS not reporting a recording afterwards, is an error.
func (r *Recorder) startPiece(ctx context.Context) error {
	var problems []string
	note := func(what string, err error) {
		problem := what + ": " + err.Error()
		r.cfg.Log("obs setup problem, recording anyway: %s", problem)
		problems = append(problems, problem)
	}
	profileProblems, err := r.ApplyProfile(ctx)
	problems = append(problems, profileProblems...)
	if err != nil {
		note("profile", err)
	}
	if err := r.setRecordDirectory(ctx); err != nil {
		note("record folder", err)
	}
	if err := r.setUpScene(ctx); err != nil {
		note("scene", err)
	}
	if err := r.muteGlobalAudio(ctx); err != nil {
		note("muting desktop and mic audio", err)
	}
	r.mu.Lock()
	r.problems = problems
	r.mu.Unlock()

	r.markExistingFilesSeen()
	client := r.currentClient()
	started := r.cfg.Now()
	if err := client.StartRecord(ctx); err != nil {
		var refused *obs.RequestError
		if !errors.As(err, &refused) || refused.Code != codeOutputRunning {
			return fmt.Errorf("starting the recording: %w", err)
		}
		// OBS is already recording, e.g. a recording this agent started before it was restarted
		r.cfg.Log("obs was already recording; carrying on with that recording")
	}
	// OBS starts its output a moment after it acknowledges StartRecord, so look for a few seconds
	var status obs.RecordStatus
	var statusErr error
	for attempt := 0; attempt < startCheckAttempts; attempt++ {
		if status, statusErr = client.RecordStatus(ctx); statusErr == nil && status.Active {
			r.curStart = started
			return nil
		}
		if err := r.cfg.Sleep(ctx, startCheckEvery); err != nil {
			return err
		}
	}
	return fmt.Errorf("obs says it is not recording after StartRecord (status %+v, error %v)", status, statusErr)
}

// setRecordDirectory points OBS at staging and checks that it took.
func (r *Recorder) setRecordDirectory(ctx context.Context) error {
	staging := r.Staging()
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return err
	}
	client := r.currentClient()
	if err := client.SetRecordDirectory(ctx, staging); err != nil {
		return fmt.Errorf("setting the record folder: %w", err)
	}
	got, err := client.RecordDirectory(ctx)
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
// cannot leave them out, and they would add room noise and echo to the class. It tries every
// input and reports the first failure.
func (r *Recorder) muteGlobalAudio(ctx context.Context) error {
	client := r.currentClient()
	names, err := client.GlobalAudioInputs(ctx)
	if err != nil {
		return fmt.Errorf("finding the global audio inputs: %w", err)
	}
	var first error
	for _, name := range names {
		if err := client.SetInputMute(ctx, name, true); err != nil && first == nil {
			first = fmt.Errorf("muting %s: %w", name, err)
		}
	}
	return first
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

// connect makes the client a live connection. With launch set it starts OBS first if OBS is not
// running. It holds no lock while it waits, so the rest of the agent keeps running.
func (r *Recorder) connect(ctx context.Context, launch bool, attempts int) error {
	if c := r.currentClient(); c != nil && c.Err() == nil {
		return nil
	}
	r.dropClient()
	launched := false
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if launch && !launched && !r.cfg.OBSRunning() {
			r.cfg.Log("obs is not running; starting it")
			if err := r.cfg.LaunchOBS(); err != nil {
				return fmt.Errorf("starting obs: %w", err)
			}
			launched = true
		}
		client, err := r.cfg.Dial(ctx)
		if err == nil {
			r.mu.Lock()
			r.client = client
			r.mu.Unlock()
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
	r.mu.Lock()
	client := r.client
	r.client = nil
	r.mu.Unlock()
	if client != nil {
		client.Close()
	}
}
