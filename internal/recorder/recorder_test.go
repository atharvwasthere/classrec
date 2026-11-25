package recorder

import (
	"context"
	"encoding/json"
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

var classStart = time.Date(2026, 11, 25, 14, 3, 0, 0, time.UTC) // a Wednesday

type harness struct {
	t          *testing.T
	fake       *obstest.Server
	rec        *Recorder
	root       string
	profileDir string

	running    atomic.Bool
	launches   atomic.Int32
	now        atomic.Pointer[time.Time]
	failMove   atomic.Bool
	failDial   atomic.Bool
	renameGate atomic.Pointer[chan struct{}] // while set, every move waits for it to close
	dialGate   atomic.Pointer[chan struct{}] // while set, every dial waits for it to close
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
			if gate := h.dialGate.Load(); gate != nil {
				select {
				case <-*gate:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			if h.failDial.Load() {
				return nil, errors.New("connection refused")
			}
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
				if gate := h.renameGate.Load(); gate != nil {
					<-*gate
				}
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

const meetFolder = "2026-11-25 Wednesday - xyz-abcd-efg (Meet)"

// end stops the class and waits for it to be filed.
func (h *harness) end(ctx context.Context) Class {
	h.t.Helper()
	filing, err := h.rec.End(ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	return filing.Class()
}

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

func readPieces(t *testing.T, dir string) piecesFile {
	t.Helper()
	var info piecesFile
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(dir, PiecesFile))), &info); err != nil {
		t.Fatal(err)
	}
	return info
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
	for key, want := range map[string]float64{
		"baseWidth": 1920, "baseHeight": 1080, "outputWidth": 1280, "outputHeight": 720, "fpsNumerator": 15, "fpsDenominator": 1,
	} {
		if video[key] != want {
			t.Errorf("video %s = %v, want %v", key, video[key], want)
		}
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

// The canvas must be the display, or a window bigger than 1280x720 is cropped; the window must be
// scaled to fit the whole canvas, shape kept, centred.
func TestWindowIsFittedToTheDisplayCanvas(t *testing.T) {
	for _, tt := range []struct {
		name         string
		w, h         int
		outW, outH   float64
		wantBoundsWH [2]float64
	}{
		{"1080p", 1920, 1080, 1280, 720, [2]float64{1920, 1080}},
		{"1366x768 laptop", 1366, 768, 1280, 720, [2]float64{1366, 768}},
		{"1440p", 2560, 1440, 1280, 720, [2]float64{2560, 1440}},
		{"16:10 keeps its shape", 1920, 1200, 1152, 720, [2]float64{1920, 1200}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.fake.SetMonitor(tt.w, tt.h)
			if err := h.rec.Begin(context.Background(), meet); err != nil {
				t.Fatal(err)
			}
			video := h.fake.Video()
			if video["baseWidth"] != float64(tt.w) || video["baseHeight"] != float64(tt.h) ||
				video["outputWidth"] != tt.outW || video["outputHeight"] != tt.outH {
				t.Errorf("video %v", video)
			}
			item := h.fake.SceneItemTransform("classrec - Meet", "classrec - Meet video")
			if item["boundsType"] != "OBS_BOUNDS_SCALE_INNER" ||
				item["boundsWidth"] != tt.wantBoundsWH[0] || item["boundsHeight"] != tt.wantBoundsWH[1] ||
				item["boundsAlignment"] != float64(0) || item["positionX"] != float64(0) || item["positionY"] != float64(0) {
				t.Errorf("transform %v", item)
			}
		})
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

// A class recorded with the wrong scene, or without muting the mic, beats no class at all.
func TestSetupFailuresAreProblemsNotReasonsToSkipTheClass(t *testing.T) {
	h := newHarness(t)
	h.fake.FailRequests("GetProfileList", "GetMonitorList", "GetSceneList", "CreateScene", "SetCurrentProgramScene",
		"GetSpecialInputs", "SetInputMute", "SetSceneItemTransform")
	ctx := context.Background()

	if err := h.rec.Begin(ctx, meet); err != nil {
		t.Fatalf("Begin failed over a setup problem: %v", err)
	}
	if !h.fake.Recording() {
		t.Fatal("not recording")
	}
	problems := strings.Join(h.rec.Problems(), "\n")
	for _, want := range []string{"profile", "scene", "muting"} {
		if !strings.Contains(problems, want) {
			t.Errorf("problems do not mention %q:\n%s", want, problems)
		}
	}
	class := h.end(ctx)
	if len(class.Files) != 1 || readFile(t, class.Files[0]) != "fake mkv recording-1.mkv" {
		t.Fatalf("class %+v", class)
	}
}

func TestEvenWhenOBSWillNotTakeTheRecordFolderItRecords(t *testing.T) {
	h := newHarness(t)
	h.fake.FailRequests("SetRecordDirectory", "GetRecordDirectory", "SetVideoSettings", "SetProfileParameter")
	if err := h.rec.Begin(context.Background(), meet); err != nil {
		t.Fatal(err)
	}
	if !h.fake.Recording() || len(h.rec.Problems()) == 0 {
		t.Fatalf("recording %v, problems %v", h.fake.Recording(), h.rec.Problems())
	}
}

func TestOnlyStartRecordFailingIsAnError(t *testing.T) {
	h := newHarness(t)
	h.fake.FailRequests("StartRecord")
	err := h.rec.Begin(context.Background(), meet)
	if err == nil || !strings.Contains(err.Error(), "starting the recording") {
		t.Fatalf("got %v", err)
	}
	if h.rec.Recording() {
		t.Fatal("marked recording although StartRecord failed")
	}
}

func TestEndFilesTheRecordingInTheClassFolder(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.rec.Begin(ctx, meet)

	class := h.end(ctx)
	wantDir := filepath.Join(h.root, meetFolder)
	if class.Dir != wantDir || len(class.Files) != 1 || class.Pending != 0 {
		t.Fatalf("class %+v", class)
	}
	if got := readFile(t, filepath.Join(wantDir, "part-01.mkv")); got != "fake mkv recording-1.mkv" {
		t.Fatalf("filed contents %q", got)
	}
	info := readPieces(t, wantDir)
	if len(info.Pieces) != 1 || info.Pieces[0].File != "part-01.mkv" || !info.Pieces[0].Start.Equal(classStart) || info.App != "Meet" {
		t.Fatalf("pieces.json %+v", info)
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

	restart := classStart.Add(12 * time.Minute)
	h.setNow(restart)
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

	class := h.end(ctx)
	if len(class.Files) != 2 || class.Pending != 0 {
		t.Fatalf("class %+v", class)
	}
	if got := readFile(t, filepath.Join(class.Dir, "part-01.mkv")); got != "fake mkv recording-1.mkv" {
		t.Errorf("part-01 holds %q, want the piece OBS died on", got)
	}
	if got := readFile(t, filepath.Join(class.Dir, "part-02.mkv")); got != "fake mkv recording-2.mkv" {
		t.Errorf("part-02 holds %q", got)
	}
	info := readPieces(t, class.Dir)
	if len(info.Pieces) != 2 || !info.Pieces[0].Start.Equal(classStart) || !info.Pieces[1].Start.Equal(restart) {
		t.Errorf("pieces.json %+v", info)
	}
	if left := h.stagingFiles(); len(left) != 0 {
		t.Fatalf("staging still holds %v", left)
	}
}

func TestOBSGoneForGoodStillFilesWhatWasRecorded(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.rec.Begin(ctx, meet)
	h.fake.Kill()
	h.running.Store(false)
	h.failDial.Store(true)

	class := h.end(ctx)
	if len(class.Files) != 1 || readFile(t, class.Files[0]) != "fake mkv recording-1.mkv" {
		t.Fatalf("class %+v", class)
	}
	if h.launches.Load() != 0 {
		t.Fatal("End launched obs; only Begin and Tick may")
	}
}

// If only the connection died, End must reconnect and stop the recording properly rather than
// guess from the files in staging.
func TestEndWithADeadConnectionReconnectsAndStopsOBS(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.rec.Begin(ctx, meet)
	h.fake.DropConnections() // OBS keeps running and recording
	waitFor(t, "client noticing", func() bool { return h.rec.currentClient().Err() != nil })

	class := h.end(ctx)
	if len(class.Files) != 1 || readFile(t, class.Files[0]) != "fake mkv recording-1.mkv" {
		t.Fatalf("class %+v", class)
	}
	if h.fake.Recording() {
		t.Fatal("obs is still recording: End never stopped it")
	}
	if h.launches.Load() != 0 {
		t.Fatal("launched obs")
	}
	var stops int
	for _, r := range h.fake.Requests() {
		if r == "StopRecord" {
			stops++
		}
	}
	if stops != 1 {
		t.Fatalf("StopRecord sent %d times", stops)
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
	class := h.end(ctx)
	if len(class.Files) != 1 {
		t.Fatalf("class %+v", class)
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

// within fails the test if f takes longer than a second.
func within(t *testing.T, what string, f func()) {
	t.Helper()
	finished := make(chan struct{})
	go func() { f(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatalf("%s took more than a second", what)
	}
}

func TestFileStaysInStagingWhenOBSKeepsHoldingIt(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.rec.Begin(ctx, meet)
	h.failMove.Store(true)

	class := h.end(ctx)
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

// Class two starts while class one's file is still being moved, so a slow release by OBS never
// costs the start of the next class.
func TestNextClassBeginsWhileThePreviousIsStillBeingFiled(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	gate := make(chan struct{})
	h.renameGate.Store(&gate)

	if err := h.rec.Begin(ctx, meet); err != nil {
		t.Fatal(err)
	}
	var first *Filing
	within(t, "End", func() {
		var err error
		if first, err = h.rec.End(ctx); err != nil {
			t.Error(err)
		}
	})
	within(t, "Begin of the next class", func() {
		if err := h.rec.Begin(ctx, meet); err != nil {
			t.Error(err)
		}
	})
	select {
	case <-first.Done():
		t.Fatal("the first class finished filing although its move was blocked")
	default:
	}
	if !h.fake.Recording() {
		t.Fatal("the second class is not being recorded")
	}

	close(gate)
	var second *Filing
	within(t, "End of the second class", func() {
		var err error
		if second, err = h.rec.End(ctx); err != nil {
			t.Error(err)
		}
	})
	one, two := first.Class(), second.Class()
	if one.Dir == two.Dir || len(one.Files) != 1 || len(two.Files) != 1 {
		t.Fatalf("classes %+v and %+v", one, two)
	}
	if readFile(t, one.Files[0]) != "fake mkv recording-1.mkv" || readFile(t, two.Files[0]) != "fake mkv recording-2.mkv" {
		t.Fatal("recordings ended up in the wrong class")
	}
	h.rec.Wait(ctx)
}

// While Begin waits for OBS to answer, the rest of the agent must not freeze.
func TestStatusQueriesAnswerWhileBeginIsConnecting(t *testing.T) {
	h := newHarness(t)
	gate := make(chan struct{})
	h.dialGate.Store(&gate)
	begun := make(chan error, 1)
	go func() { begun <- h.rec.Begin(context.Background(), meet) }()
	time.Sleep(50 * time.Millisecond) // Begin is now inside Dial

	within(t, "Recording and AppAudioLive", func() {
		if h.rec.Recording() || h.rec.AppAudioLive() {
			t.Error("recording before OBS answered")
		}
		h.rec.Problems()
	})
	close(gate)
	if err := <-begun; err != nil {
		t.Fatal(err)
	}
}
