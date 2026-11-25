package recorder

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"classrec/internal/detect"
	"classrec/internal/obs"
	"classrec/internal/obs/obstest"
)

const testPassword = "pw"

var classStart = time.Date(2026, 11, 25, 14, 3, 0, 0, time.UTC)

type harness struct {
	t          *testing.T
	fake       *obstest.Server
	rec        *Recorder
	root       string
	profileDir string

	running  atomic.Bool
	launches atomic.Int32
	now      atomic.Pointer[time.Time]
	failMove atomic.Bool
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, fake: obstest.New(testPassword), root: t.TempDir(), profileDir: t.TempDir()}
	t.Cleanup(h.fake.Close)
	h.running.Store(true)
	h.setNow(classStart)
	h.rec = New(Config{
		Root:       h.root,
		ProfileDir: h.profileDir,
		Dial: func(ctx context.Context) (OBS, error) {
			c, err := obs.Connect(ctx, h.fake.URL(), testPassword)
			if err != nil {
				return nil, err
			}
			return c, nil
		},
		OBSRunning: h.running.Load,
		LaunchOBS: func() error {
			h.launches.Add(1)
			h.running.Store(true)
			return nil
		},
		ConnectAttempts: 3,
		Now:             func() time.Time { return *h.now.Load() },
		Sleep:           func(ctx context.Context, d time.Duration) error { return ctx.Err() },
		Move: MoveOptions{
			First: time.Millisecond, Total: 4 * time.Millisecond,
			rename: func(from, to string) error {
				if h.failMove.Load() {
					return errors.New("the process cannot access the file because it is being used by another process")
				}
				return os.Rename(from, to)
			},
		},
		Log: func(format string, a ...any) { t.Logf(format, a...) },
	})
	t.Cleanup(func() { h.rec.dropClient() })
	return h
}

func (h *harness) setNow(t time.Time) { h.now.Store(&t) }

var meet = Target{App: detect.Meet, Title: "Meet - xyz-abcd-efg - Personal - Microsoft Edge", Class: "Chrome_WidgetWin_1", Exe: "msedge.exe"}

func (h *harness) stagingFiles() []string {
	entries, _ := os.ReadDir(filepath.Join(h.root, "_staging"))
	var names []string
	for _, e := range entries {
		if isRecording(e.Name()) {
			names = append(names, e.Name())
		}
	}
	return names
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestBeginSetsOBSUpTheSameWayEveryTime(t *testing.T) {
	h := newHarness(t)
	if err := h.rec.Begin(context.Background(), meet); err != nil {
		t.Fatal(err)
	}
	if !h.fake.Recording() {
		t.Fatal("not recording")
	}
	if got := h.fake.Profile(); got != "classrec" {
		t.Errorf("profile %q", got)
	}
	for name, want := range map[string]string{
		"Output/Mode": "Advanced", "AdvOut/RecFormat2": "mkv", "AdvOut/RecEncoder": "obs_qsv11_v2", "AdvOut/RecRescale": "false",
	} {
		cat, key, _ := strings.Cut(name, "/")
		if got, _ := h.fake.Param("classrec", cat, key); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	video := h.fake.Video()
	if video["outputWidth"] != float64(1280) || video["outputHeight"] != float64(720) || video["fpsNumerator"] != float64(15) {
		t.Errorf("video %v", video)
	}
	if got := h.fake.RecordDirectory(); got != filepath.Join(h.root, "_staging") {
		t.Errorf("record directory %q", got)
	}
	if got := h.fake.CurrentScene(); got != "classrec - Meet" {
		t.Errorf("scene %q", got)
	}
	in, ok := h.fake.Input("classrec - Meet video")
	if !ok || in.Settings["window"] != "Meet - xyz-abcd-efg - Personal - Microsoft Edge:Chrome_WidgetWin_1:msedge.exe" {
		t.Errorf("video input %+v", in)
	}
	if _, ok := h.fake.Input("classrec - Meet audio"); !ok {
		t.Error("no audio input")
	}
	for _, name := range []string{"Desktop Audio", "Mic/Aux"} {
		if !h.fake.IsMuted(name) {
			t.Errorf("%s not muted", name)
		}
	}
	encoder := readFile(t, filepath.Join(h.profileDir, "recordEncoder.json"))
	for _, want := range []string{`"rate_control": "CBR"`, `"bitrate": 1000`, `"keyint_sec": 2`} {
		if !strings.Contains(encoder, want) {
			t.Errorf("recordEncoder.json lacks %s:\n%s", want, encoder)
		}
	}
	if problems := h.rec.Problems(); len(problems) > 0 {
		t.Errorf("problems: %v", problems)
	}
}

func TestZoomUsesItsOwnScene(t *testing.T) {
	h := newHarness(t)
	zoom := Target{App: detect.Zoom, Title: "Zoom Meeting"}
	if err := h.rec.Begin(context.Background(), zoom); err != nil {
		t.Fatal(err)
	}
	in, _ := h.fake.Input("classrec - Zoom video")
	if h.fake.CurrentScene() != "classrec - Zoom" || in.Settings["window"] != "Zoom Meeting:ConfMultiTabContentWndClass:Zoom.exe" {
		t.Fatalf("scene %q, video input %+v", h.fake.CurrentScene(), in)
	}
}

func TestWindowSpecEscapesColonsAndHashes(t *testing.T) {
	got := windowSpec(Target{App: detect.Meet, Title: "Class: Maths #1", Class: "C", Exe: "msedge.exe"})
	if want := "Class#3A Maths #231:C:msedge.exe"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestEndFilesTheRecordingInADatedClassFolder(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.rec.Begin(ctx, meet)

	class, err := h.rec.End(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantDir := filepath.Join(h.root, "2026-11-25", "2026-11-25 14-03-00 Meet")
	if class.Dir != wantDir || len(class.Files) != 1 || class.Pending != 0 {
		t.Fatalf("class %+v", class)
	}
	if got := readFile(t, filepath.Join(wantDir, "part-01.mkv")); got != "fake mkv recording-1.mkv" {
		t.Fatalf("filed contents %q", got)
	}
	if left := h.stagingFiles(); len(left) != 0 {
		t.Fatalf("staging still holds %v", left)
	}
	if h.rec.Recording() {
		t.Fatal("still marked recording")
	}
}

func TestOBSKilledMidClassGivesASecondPieceOfTheSameClass(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.rec.Begin(ctx, meet)

	h.fake.Kill()
	h.running.Store(false)
	if err := h.rec.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if h.launches.Load() != 1 {
		t.Fatalf("obs launched %d times, want once", h.launches.Load())
	}
	if !h.fake.Recording() {
		t.Fatal("not recording again after OBS came back")
	}
	if h.fake.CurrentScene() != "classrec - Meet" || h.fake.RecordDirectory() != filepath.Join(h.root, "_staging") {
		t.Fatal("setup was not reapplied")
	}

	class, err := h.rec.End(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(class.Files) != 2 || class.Pending != 0 {
		t.Fatalf("class %+v", class)
	}
	if got := readFile(t, filepath.Join(class.Dir, "part-01.mkv")); got != "fake mkv recording-1.mkv" {
		t.Errorf("part-01 holds %q, want the piece OBS died on", got)
	}
	if got := readFile(t, filepath.Join(class.Dir, "part-02.mkv")); got != "fake mkv recording-2.mkv" {
		t.Errorf("part-02 holds %q", got)
	}
	if left := h.stagingFiles(); len(left) != 0 {
		t.Fatalf("staging still holds %v", left)
	}
}

func TestOBSKilledAndNeverBackStillFilesWhatWasRecorded(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.rec.Begin(ctx, meet)
	h.fake.Kill()

	class, err := h.rec.End(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(class.Files) != 1 || readFile(t, class.Files[0]) != "fake mkv recording-1.mkv" {
		t.Fatalf("class %+v", class)
	}
}

func TestLostConnectionAloneKeepsTheSameRecording(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.rec.Begin(ctx, meet)

	h.fake.DropConnections() // OBS keeps running and recording
	if err := h.rec.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if h.launches.Load() != 0 {
		t.Fatal("launched obs although it was running")
	}
	starts := 0
	for _, r := range h.fake.Requests() {
		if r == "StartRecord" {
			starts++
		}
	}
	if starts != 1 {
		t.Fatalf("StartRecord sent %d times", starts)
	}
	class, err := h.rec.End(ctx)
	if err != nil || len(class.Files) != 1 {
		t.Fatalf("class %+v, err %v", class, err)
	}
}

func TestTickWhileHealthyDoesNothing(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.rec.Begin(ctx, meet)
	before := len(h.fake.Requests())
	if err := h.rec.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	for _, r := range h.fake.Requests()[before:] {
		if r != "GetRecordStatus" {
			t.Fatalf("a healthy tick sent %s", r)
		}
	}
}

func TestBeginStartsOBSWhenItIsNotRunning(t *testing.T) {
	h := newHarness(t)
	h.running.Store(false)
	if err := h.rec.Begin(context.Background(), meet); err != nil {
		t.Fatal(err)
	}
	if h.launches.Load() != 1 {
		t.Fatalf("launches %d", h.launches.Load())
	}
}

func TestSecondBeginIsRefused(t *testing.T) {
	h := newHarness(t)
	h.rec.Begin(context.Background(), meet)
	if err := h.rec.Begin(context.Background(), meet); err == nil {
		t.Fatal("second Begin succeeded")
	}
}

func TestAppAudioLiveFollowsTheAppAudioInput(t *testing.T) {
	h := newHarness(t)
	h.rec.Begin(context.Background(), meet)
	h.setNow(time.Now())

	if h.rec.AppAudioLive() {
		t.Fatal("live before any level")
	}
	h.fake.SendLevel("classrec - Meet audio", 0.1) // -20 dB: someone talking
	waitFor(t, "audible", h.rec.AppAudioLive)

	h.fake.SendLevel("classrec - Meet audio", 0.00005) // -86 dB: silence
	waitFor(t, "silent", func() bool { return !h.rec.AppAudioLive() })

	h.fake.SendLevel("classrec - Meet audio", 0.1)
	waitFor(t, "audible again", h.rec.AppAudioLive)
	h.setNow(time.Now().Add(time.Minute)) // no level for a minute: not a level any more
	if h.rec.AppAudioLive() {
		t.Fatal("a stale level counted")
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("never became %s", what)
}

func TestFileStaysInStagingWhenOBSKeepsHoldingIt(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.rec.Begin(ctx, meet)
	h.failMove.Store(true)

	class, err := h.rec.End(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if class.Pending != 1 || len(class.Files) != 0 {
		t.Fatalf("class %+v", class)
	}
	if got := h.stagingFiles(); len(got) != 1 {
		t.Fatalf("staging holds %v, want the recording left alone", got)
	}
	if len(readPending(h.rec.Staging())) != 1 {
		t.Fatal("the move was not remembered")
	}

	// the next agent start, with OBS no longer holding the file
	h.failMove.Store(false)
	moved, still := h.rec.RetryPending(ctx)
	if moved != 1 || still != 0 {
		t.Fatalf("moved %d, still %d", moved, still)
	}
	if got := readFile(t, filepath.Join(class.Dir, "part-01.mkv")); got != "fake mkv recording-1.mkv" {
		t.Fatalf("retried file holds %q", got)
	}
	if len(h.stagingFiles()) != 0 || len(readPending(h.rec.Staging())) != 0 {
		t.Fatal("staging or the pending list not cleaned up")
	}
}
