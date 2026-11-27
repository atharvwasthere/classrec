package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"classrec/internal/desktop"
	"classrec/internal/detect"
	"classrec/internal/obs"
	"classrec/internal/overlay"
	"classrec/internal/recorder"
)

const defaultRoot = `D:\Class Recordings` // on D:, so classes never fill the system drive

type recordOptions struct {
	Mode     modeChoice
	Interval time.Duration
	Root     string
	OBSURL   string
}

// agent is the record loop's state: it watches for classes, offers to record each one, and
// records with OBS the ones she says yes to.
type agent struct {
	opts    recordOptions
	rec     *recorder.Recorder
	machine *detect.Machine
	log     *agentLog
	pill    *overlay.Overlay // nil if the overlay could not start
	warn    *warnings        // nil between classes
	level   overlay.Level
	// wanted is set when she clicks "Record this class", on the overlay's thread, and cleared
	// when the class ends. Only a wanted class is recorded.
	wanted  atomic.Bool
	offered bool // the "Record this class" card is on screen
	// videoCheckedAt is when OBS's picture of the class was last checked; blankChecks counts the
	// checks in a row that found no picture.
	videoCheckedAt time.Time
	blankChecks    int
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
	a.pill, err = overlay.New(overlay.Config{SettingsPath: settingsPath(), OnRecord: a.record})
	if err != nil {
		log.printf("no overlay, so every class records without asking: %v", err)
	}
	log.printf("agent started: mode %s (%s), recordings in %s, silence below %.0f dB", opts.Mode.Mode, opts.Mode.Source, opts.Root, silenceDB())
	if moved, still := rec.RetryPending(ctx); moved+still > 0 {
		log.printf("files left in staging by an earlier run: %d filed, %d still stuck", moved, still)
	}
	if class, ok, err := rec.StopLeftover(ctx); ok {
		log.printf("an earlier run left obs recording; stopped it and filed it in %s (%d file(s), %d left in staging)", class.Dir, len(class.Files), class.Pending)
	} else if err != nil {
		log.printf("checking for a recording an earlier run left going: %v", err)
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
			if a.update(ctx, updates) {
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

// update installs a newer release, if there is one, and queues a run of the scheduled task behind
// this one, so the agent starts again on the new exe as soon as it exits. It reports whether to exit.
func (a *agent) update(ctx context.Context, updates *updater) bool {
	updated, err := updates.update(ctx)
	if err != nil {
		a.log.printf("update check: %v", err)
		return false
	}
	if updated {
		if err := schtasks(ctx, "/Run", "/TN", taskName); err != nil {
			a.log.printf("could not queue the restart; the agent starts again at next logon: %v", err)
		}
	}
	return updated
}

// step takes one look at the desktop and acts on it.
func (a *agent) step(ctx context.Context) {
	sample := desktop.Snapshot(time.Now())
	if a.rec.Recording() {
		sample.AppAudioLive = a.rec.AppAudioLive()
	}
	observed := detect.Classify(sample)
	event := a.machine.Step(sample.Time, observed)
	a.log.sample(debugLine{Sample: sample.ClassAppsOnly(), Obs: observed, Phase: a.machine.Phase(), Event: event})
	switch {
	case event != nil && event.Kind == detect.Stop:
		a.endClass(ctx)
	case event != nil && event.Kind == detect.Start:
		a.offerClass(event.App, event.Title)
	case a.rec.Recording():
		app, _ := a.machine.Class()
		title, class := classWindow(app, observed, sample)
		if err := a.rec.Follow(ctx, title, class); err != nil {
			a.log.printf("could not point obs at the renamed class window: %v", err)
		}
		if err := a.rec.Tick(ctx); err != nil {
			a.log.printf("could not keep the recording going: %v", err)
		}
		a.updatePill(ctx, sample)
	case a.wanted.Load() && (a.machine.Phase() == detect.Recording || a.machine.Phase() == detect.Ending):
		// She asked to record it, or a start failed earlier: (re)try, naming the class after its window
		// now. At detection Meet may still be in its lobby, titled only "Meet".
		app, title := a.machine.Class()
		target := recorder.Target{App: app, Title: title}
		if now, class := classWindow(app, observed, sample); now != "" {
			target.Title, target.Class = now, class
		}
		a.beginClass(ctx, target)
	case a.machine.Phase() == detect.Recording || a.machine.Phase() == detect.Ending:
		app, _ := a.machine.Class() // not asked to record: the card shows only while the meeting window does
		a.showOffer(app, observed.Meeting[app] != "")
	}
}

// offerClass shows the card with "Record this class". Nothing records until she clicks it; without
// an overlay there is no way to ask, so the class records as if she had.
func (a *agent) offerClass(app detect.App, title string) {
	a.log.printf("class detected: %s %q; asking whether to record", app, title)
	a.wanted.Store(a.pill == nil) // also drops a click that came as the last class ended
	a.offered = false
	a.showOffer(app, true)
}

// showOffer shows or hides the "Record this class" card, so it leaves with the meeting window and
// comes back if she rejoins before the class ends.
func (a *agent) showOffer(app detect.App, show bool) {
	if a.pill == nil || show == a.offered {
		return
	}
	a.offered = show
	if !show {
		a.pill.Hide()
		return
	}
	a.pill.SetStatus(overlay.Offer, string(app)+" class detected")
	a.pill.Show()
}

// record is her "Record this class" click. It runs on the overlay's thread, so it only flags the
// class; the next step starts the recording.
func (a *agent) record() {
	if a.wanted.Swap(true) {
		return // a second click
	}
	a.pill.SetStatus(overlay.Offer, "Starting the recording…")
}

func (a *agent) beginClass(ctx context.Context, target recorder.Target) {
	a.log.startClass(classFile(a.opts.Root, "class.log"))
	if err := a.rec.Begin(ctx, target); err != nil {
		a.log.printf("could not start recording: %v", err)
		return
	}
	a.log.printf("recording started; mode %s", a.opts.Mode.Mode)
	desktop.KeepAwake(true)
	a.warn, a.level = newWarnings(time.Now()), overlay.Normal
	a.videoCheckedAt, a.blankChecks = time.Now(), 0 // first look 30 s in
	if a.pill != nil {
		a.pill.SetStatus(overlay.Normal, "")
		a.pill.Show()
	}
}

func (a *agent) endClass(ctx context.Context) {
	wanted := a.wanted.Swap(false)
	a.offered = false
	if a.pill != nil {
		a.pill.Hide()
	}
	if !a.rec.Recording() {
		if !wanted {
			a.log.printf("class ended; not recorded, as she did not ask")
		}
		return
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
		moveIfPresent(classFile(a.opts.Root, "class.log"), filepath.Join(class.Dir, "class.log"), a.log.printf)
		copyToReports(a.opts.Root, filepath.Join(class.Dir, "class.log"), "class "+filepath.Base(class.Dir)+".log")
		copyToReports(a.opts.Root, a.log.detectPath(), "")
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
		videoBlank: a.checkVideo(ctx, sample.Time),
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

// checkVideo looks at OBS's picture of the class every 30 s and reports whether it has shown
// nothing for two looks in a row. At the second it points the capture at the window again, which
// is what a lost window needs.
func (a *agent) checkVideo(ctx context.Context, now time.Time) bool {
	const every, blankAfter = 30 * time.Second, 2
	if now.Sub(a.videoCheckedAt) < every {
		return a.blankChecks >= blankAfter
	}
	a.videoCheckedAt = now
	if !blankVideo(a.rec.VideoFrame(ctx)) {
		if a.blankChecks >= blankAfter {
			a.log.printf("the class video is back")
		}
		a.blankChecks = 0
		return false
	}
	a.blankChecks++
	if a.blankChecks == blankAfter {
		a.log.printf("obs has recorded no picture of the class window for a minute; pointing it at the window again")
		if err := a.rec.Recapture(ctx); err != nil {
			a.log.printf("could not point obs at the class window: %v", err)
		}
	}
	return a.blankChecks >= blankAfter
}

// classFile is where a file of the class in progress lives until the class is filed.
func classFile(root, name string) string { return filepath.Join(root, "_staging", name) }

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

// classWindow is the window to capture now: the app's meeting window, or, when Edge shows another
// tab mid-class, Edge's visible window, so the recording follows what she sees. "" when neither.
func classWindow(app detect.App, observed detect.Observation, sample detect.Sample) (title, class string) {
	spec, _ := detect.SpecFor(app)
	meeting := observed.Meeting[app]
	for _, w := range sample.Windows {
		sameApp := strings.EqualFold(filepath.Base(w.Exe), spec.Exe) && w.Class == spec.WindowClass
		if meeting != "" && w.Title == meeting || meeting == "" && sameApp && w.Visible && !w.Minimized && w.Title != "" {
			return w.Title, w.Class
		}
	}
	return "", ""
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
