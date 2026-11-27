package recorder

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"classrec/internal/detect"
	"classrec/internal/obs"
	"classrec/internal/obs/obstest"
)

const password = "pw"

var classStart = time.Date(2026, 11, 25, 14, 3, 0, 0, time.UTC) // a Wednesday

var meet = Target{App: detect.Meet, Title: "Meet - xyz-abcd-efg - Personal - Microsoft Edge"}

// harness is a recorder wired to a fake OBS that has the classrec profile install writes.
type harness struct {
	t    *testing.T
	fake *obstest.Server
	rec  *Recorder
	root string

	running   bool // obs64.exe is running
	dying     bool // obs64.exe still shows but refuses the next connection, then is gone
	failDial  bool // OBS refuses connections
	holdFiles bool // OBS keeps its files open, so renames fail
	launches  int
	now       time.Time
}

func newHarness(t *testing.T, canvasW, canvasH int) *harness {
	h := &harness{t: t, fake: obstest.New(password), root: t.TempDir(), running: true, now: classStart}
	t.Cleanup(h.fake.Close)
	h.fake.AddProfile(ProfileName, obstest.Profile{
		Params: map[string]string{"Output/Mode": "Advanced", "AdvOut/RecFormat2": "mkv", "AdvOut/RecEncoder": Encoder},
		Video: map[string]any{"fpsNumerator": FPS, "fpsDenominator": 1, "baseWidth": canvasW, "baseHeight": canvasH,
			"outputWidth": OutputWidth, "outputHeight": OutputHeight},
	})
	h.rec = New(Config{
		Root: h.root,
		Dial: func(ctx context.Context) (*obs.Client, error) {
			if h.dying {
				h.dying, h.running = false, false
				return nil, errors.New("connection refused")
			}
			if h.failDial {
				return nil, errors.New("connection refused")
			}
			return obs.Connect(ctx, h.fake.URL(), password)
		},
		OBSRunning: func() bool { return h.running },
		LaunchOBS:  func() error { h.launches++; h.running = true; return nil },
		Now:        func() time.Time { return h.now },
		Sleep:      func(context.Context, time.Duration) error { return nil },
		Log:        t.Logf,
		rename: func(from, to string) error {
			if h.holdFiles {
				return errors.New("the process cannot access the file because it is being used by another process")
			}
			return os.Rename(from, to)
		},
	})
	t.Cleanup(h.rec.drop)
	return h
}

func (h *harness) begin(t Target) {
	h.t.Helper()
	if err := h.rec.Begin(context.Background(), t); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) end() Class {
	h.t.Helper()
	class, err := h.rec.End(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	return class
}

func (h *harness) staging() []string {
	entries, _ := os.ReadDir(h.rec.staging())
	var names []string
	for _, e := range entries {
		if isRecording(e.Name()) {
			names = append(names, e.Name())
		}
	}
	return names
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func count(list []string, want string) (n int) {
	for _, s := range list {
		if s == want {
			n++
		}
	}
	return n
}

// Promise: each app is recorded from its own scene, with its meeting window scaled to fit the
// whole canvas, the record folder set to staging, and Desktop Audio and Mic/Aux muted.
func TestEachAppIsRecordedWholeFromItsOwnScene(t *testing.T) {
	for _, tt := range []struct {
		target     Target
		canvas     [2]int
		wantWindow string
	}{
		{meet, [2]int{1920, 1080}, "Meet - xyz-abcd-efg - Personal - Microsoft Edge:Chrome_WidgetWin_1:msedge.exe"},
		{Target{App: detect.Zoom, Title: "Zoom Meeting: Maths #1"}, [2]int{1920, 1200}, "Zoom Meeting#3A Maths #231:ConfMultiTabContentWndClass:Zoom.exe"},
	} {
		t.Run(string(tt.target.App), func(t *testing.T) {
			h := newHarness(t, tt.canvas[0], tt.canvas[1])
			h.begin(tt.target)
			checkRecordedWhole(t, h, "classrec - "+string(tt.target.App), tt.canvas, tt.wantWindow)
		})
	}
}

func checkRecordedWhole(t *testing.T, h *harness, scene string, canvas [2]int, wantWindow string) {
	st := h.fake.State()
	if !st.Recording || st.Profile != ProfileName || st.Scene != scene || st.RecordDir != h.rec.staging() {
		t.Fatalf("state %+v", st)
	}
	if got := st.Inputs[scene+" video"].Settings["window"]; got != wantWindow {
		t.Errorf("window %q, want %q", got, wantWindow)
	}
	if st.Inputs[scene+" audio"].Kind != "wasapi_process_output_capture" {
		t.Error("no app audio capture")
	}
	fit := st.Transforms[scene+"/"+scene+" video"]
	if fit["boundsType"] != "OBS_BOUNDS_SCALE_INNER" || fit["boundsWidth"] != float64(canvas[0]) ||
		fit["boundsHeight"] != float64(canvas[1]) || fit["boundsAlignment"] != float64(0) {
		t.Errorf("window not fitted to the %v canvas: %v", canvas, fit)
	}
	if !st.Muted["Desktop Audio"] || !st.Muted["Mic/Aux"] {
		t.Errorf("muted %v", st.Muted)
	}
	if p := h.rec.Problems(); len(p) > 0 {
		t.Errorf("problems with an installed profile: %v", p)
	}
}

// Promise: when Edge renames the class window (lobby to call, or another tab), picture and sound
// follow it, since OBS finds an Edge window only by its exact title.
func TestCaptureFollowsTheRenamedClassWindow(t *testing.T) {
	h := newHarness(t, 1920, 1080)
	h.begin(Target{App: detect.Meet, Title: "Meet - Preeti - Microsoft Edge"})
	if err := h.rec.Follow(context.Background(), "Meet - xyz-abcd-efg - Preeti - Microsoft Edge", "Chrome_WidgetWin_1"); err != nil {
		t.Fatal(err)
	}
	st := h.fake.State()
	for _, input := range []string{"classrec - Meet video", "classrec - Meet audio"} {
		if got := st.Inputs[input].Settings["window"]; got != "Meet - xyz-abcd-efg - Preeti - Microsoft Edge:Chrome_WidgetWin_1:msedge.exe" {
			t.Errorf("%s captures %q", input, got)
		}
	}
}

// Promise: a class is never skipped over a setup step OBS refuses; only StartRecord failing stops it.
func TestSetupProblemsNeverSkipAClass(t *testing.T) {
	h := newHarness(t, 1920, 1080)
	h.fake.FailRequests("SetCurrentProfile", "CreateScene", "GetSpecialInputs")
	h.begin(meet)
	if !h.fake.State().Recording {
		t.Fatal("not recording")
	}
	problems := strings.Join(h.rec.Problems(), "\n")
	for _, want := range []string{"profile", "scene", "muting"} {
		if !strings.Contains(problems, want) {
			t.Errorf("problems do not mention %s:\n%s", want, problems)
		}
	}
	if class := h.end(); len(class.Files) != 1 {
		t.Fatalf("class %+v", class)
	}

	refused := newHarness(t, 1920, 1080)
	refused.fake.FailRequests("StartRecord")
	if err := refused.rec.Begin(context.Background(), meet); err == nil || refused.rec.Recording() {
		t.Fatalf("StartRecord refused, yet Begin returned %v and Recording is %v", err, refused.rec.Recording())
	}
}

// Promise: if OBS dies mid-class it is started again and the class continues in a second piece,
// filed in the same folder, with each piece's start noted.
func TestOBSKilledMidClassGivesTwoPiecesInOneFolder(t *testing.T) {
	h := newHarness(t, 1920, 1080)
	h.begin(meet)
	h.now = classStart.Add(12 * time.Minute)
	h.fake.Kill()
	h.dying = true       // obs64.exe is still in the process list for a moment after it is killed
	h.fake.StartingUp(3) // and the relaunched OBS is not ready to record at first
	if err := h.rec.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h.launches != 1 || !h.fake.State().Recording {
		t.Fatalf("launches %d, recording %v: a dying OBS must be relaunched in the same Tick", h.launches, h.fake.State().Recording)
	}

	class := h.end()
	if len(class.Files) != 2 || read(t, filepath.Join(class.Dir, "part-01.mkv")) != "fake mkv recording-1.mkv" ||
		read(t, filepath.Join(class.Dir, "part-02.mkv")) != "fake mkv recording-2.mkv" {
		t.Fatalf("class %+v", class)
	}
	var info Pieces
	if err := json.Unmarshal([]byte(read(t, filepath.Join(class.Dir, PiecesFile))), &info); err != nil {
		t.Fatal(err)
	}
	if len(info.Pieces) != 2 || !info.Pieces[0].Start.Equal(classStart) || !info.Pieces[1].Start.Equal(h.now) {
		t.Fatalf("pieces.json %+v", info)
	}
	if left := h.staging(); len(left) != 0 {
		t.Fatalf("staging still holds %v", left)
	}
}

// Promise: a class is filed even when OBS is gone for good at the end, and End never starts OBS.
func TestOBSGoneAtEndStillFilesTheClass(t *testing.T) {
	h := newHarness(t, 1920, 1080)
	h.begin(meet)
	h.fake.Kill()
	h.running, h.failDial = false, true
	class := h.end()
	if len(class.Files) != 1 || read(t, class.Files[0]) != "fake mkv recording-1.mkv" || h.launches != 0 {
		t.Fatalf("class %+v, launches %d", class, h.launches)
	}
}

// Promise: when only the connection dropped, End reconnects and stops OBS rather than leaving it recording.
func TestEndReconnectsToStopOBSAfterADroppedConnection(t *testing.T) {
	h := newHarness(t, 1920, 1080)
	h.begin(meet)
	h.fake.DropConnections() // OBS keeps running and recording
	for deadline := time.Now().Add(5 * time.Second); h.rec.client.Err() == nil && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	class := h.end()
	st := h.fake.State()
	if len(class.Files) != 1 || st.Recording || h.launches != 0 || count(st.Requests, "StopRecord") != 1 {
		t.Fatalf("class %+v, still recording %v, launches %d, requests %v", class, st.Recording, h.launches, st.Requests)
	}
}

// Promise: a recording is never overwritten and never deleted.
func TestMovingNeverOverwritesOrDeletes(t *testing.T) {
	for _, tt := range []struct {
		name     string
		existing []string // already in the class folder
		want     string
	}{
		{"free name", nil, "part-01.mkv"},
		{"name taken", []string{"part-01.mkv"}, "part-01 (2).mkv"},
		{"two names taken", []string{"part-01.mkv", "part-01 (2).mkv"}, "part-01 (3).mkv"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, 1920, 1080)
			dir := t.TempDir()
			for _, name := range tt.existing {
				os.WriteFile(filepath.Join(dir, name), []byte("old "+name), 0o644)
			}
			src := filepath.Join(t.TempDir(), "new.mkv")
			os.WriteFile(src, []byte("new"), 0o644)

			got, err := h.rec.move(context.Background(), src, filepath.Join(dir, "part-01.mkv"))
			if err != nil || filepath.Base(got) != tt.want || read(t, got) != "new" {
				t.Fatalf("moved to %q (%v), want %s", got, err, tt.want)
			}
			for _, name := range tt.existing {
				if read(t, filepath.Join(dir, name)) != "old "+name {
					t.Errorf("%s changed", name)
				}
			}
		})
	}

	h := newHarness(t, 1920, 1080)
	h.holdFiles = true
	src := filepath.Join(t.TempDir(), "held.mkv")
	os.WriteFile(src, []byte("precious"), 0o644)
	if _, err := h.rec.move(context.Background(), src, filepath.Join(t.TempDir(), "x.mkv")); err == nil || read(t, src) != "precious" {
		t.Fatal("a failed move must leave the file untouched")
	}
}

// Promise: a file OBS still holds stays in staging, is noted in pending.json, and is filed at the next start.
func TestHeldFileIsFiledAtTheNextStart(t *testing.T) {
	h := newHarness(t, 1920, 1080)
	h.begin(meet)
	h.holdFiles = true
	class := h.end()
	if class.Pending != 1 || len(h.staging()) != 1 || len(readPending(h.rec.staging())) != 1 {
		t.Fatalf("class %+v, staging %v", class, h.staging())
	}

	h.holdFiles = false
	if moved, still := h.rec.RetryPending(context.Background()); moved != 1 || still != 0 {
		t.Fatalf("moved %d, still %d", moved, still)
	}
	if read(t, filepath.Join(class.Dir, "part-01.mkv")) != "fake mkv recording-1.mkv" || len(h.staging()) != 0 {
		t.Fatal("not filed")
	}
}

// Promise: class folders are "<date> <day> - <title> (<app>)" with a safe, short title, and two
// classes never share one.
func TestClassFolders(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"Meet - xyz-abcd-efg - Personal - Microsoft\u200b Edge", "xyz-abcd-efg"},
		{"Meet - Maths 101 - Microsoft Edge", "Maths 101"},
		{"Meet - Algebra - Part 2 - Personal - Microsoft Edge", "Algebra - Part 2"},
		{"Zoom Meeting", "Class"},
		{"Zoom Meeting - Algebra", "Algebra"},
		{`Maths: Algebra/Geometry? "Part" <1> | *final* \x`, "Maths AlgebraGeometry Part 1 final x"},
		{"Tab\there\x01 x\x7f", "Tabhere x"},
		{"  a    b  ", "a b"},
		{"Maths . . ", "Maths"},
		{"", "Class"},
		{"Meet - गणित कक्षा - Microsoft Edge", "गणित कक्षा"},
		{strings.Repeat("क", 70), strings.Repeat("क", 60)},
		{strings.Repeat("a", 59) + " bbb", strings.Repeat("a", 59)},
	} {
		if got := sanitizeTitle(tt.in); got != tt.want {
			t.Errorf("sanitizeTitle(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	if got := classFolderName(classStart, detect.Meet, "Meet - Maths - Personal - Microsoft Edge"); got != "2026-11-25 Wednesday - Maths (Meet)" {
		t.Errorf("folder name %q", got)
	}

	root := t.TempDir()
	first, _ := makeClassDir(root, "2026-11-25 Wednesday - Maths (Meet)")
	second, _ := makeClassDir(root, "2026-11-25 Wednesday - Maths (Meet)")
	if first == second || filepath.Base(second) != "2026-11-25 Wednesday - Maths (Meet) (2)" {
		t.Fatalf("got %q and %q", first, second)
	}
}

// Promise: a recording that a killed agent left going is stopped and filed when the agent starts
// again, and a recording someone started into another folder is left alone.
func TestLeftoverRecordingIsFiledAtTheNextStart(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, 1920, 1080)
	h.begin(meet)
	restarted := New(h.rec.cfg) // the agent was killed mid-class and started again
	class, ok, err := restarted.StopLeftover(ctx)
	if err != nil || !ok || len(class.Files) != 1 || !strings.Contains(filepath.Base(class.Dir), leftoverTitle) {
		t.Fatalf("class %+v, ok %v, err %v", class, ok, err)
	}
	if h.fake.State().Recording {
		t.Error("obs still recording")
	}

	h = newHarness(t, 1920, 1080)
	h.begin(meet)
	byHand := t.TempDir()
	if err := h.rec.client.SetRecordDirectory(ctx, byHand); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := New(h.rec.cfg).StopLeftover(ctx); ok || err != nil {
		t.Errorf("stopped a recording into %s: ok %v, err %v", byHand, ok, err)
	}
	if !h.fake.State().Recording {
		t.Error("a recording into another folder was stopped")
	}
}
