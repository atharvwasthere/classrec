// Package detect decides from window and Edge tab samples when a class starts and ends.
// Everything here is pure, so recorded --debug-detect logs replay in tests.
package detect

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
)

// Sample is one observation of the desktop, as written to the --debug-detect JSONL log.
type Sample struct {
	Time    time.Time `json:"t"`
	Windows []Window  `json:"windows"`
	// AppAudioLive is true when OBS hears sound from the class app's audio input, which the
	// recorder fills in while it records. Absent from logs made before the recorder existed.
	AppAudioLive bool `json:"audio,omitempty"`
}

// Window is a top-level window and the full path of the exe that owns it.
type Window struct {
	Exe        string `json:"exe"`
	Title      string `json:"title"`
	Class      string `json:"class"`
	Visible    bool   `json:"visible"`
	Minimized  bool   `json:"minimized,omitempty"`
	Cloaked    bool   `json:"cloaked,omitempty"`
	Foreground bool   `json:"fg,omitempty"`
	// Tabs are the names Edge gives its tabs, for an Edge window: every tab, showing or not, with
	// "- Microphone recording" or "- Camera recording" added while a tab uses them.
	Tabs []string `json:"tabs,omitempty"`
}

// App is a class app.
type App string

// The class apps.
const (
	Zoom App = "Zoom"
	Meet App = "Meet"
)

// AppSpec is everything classrec knows about one class app.
type AppSpec struct {
	App         App
	Exe         string            // file name of its exe, matched ignoring case
	WindowClass string            // window class of its meeting window, for OBS's window capture
	Matches     func(Window) bool // whether a window of Exe shows her in the call
	SceneName   string            // the OBS scene the agent records it with
}

// zoomMeetingClass is the class of Zoom's meeting window: visible while she is in a meeting, hidden
// (titled "Zoom Workplace") once she leaves and Zoom stays in the tray (seen 2026-11-26).
const zoomMeetingClass = "ConfMultiTabContentWndClass"

// zoomInCallClasses are Zoom's windows that are on screen only while she is in a meeting: the
// meeting window; the small floating video window, which replaces it when she minimises the
// meeting; and the toolbar shown while she shares her screen, when the meeting window is hidden
// too (seen 2026-11-27). A muted Zoom does not hold the mic, so the mic cannot tell.
var zoomInCallClasses = []string{zoomMeetingClass, "ZPFloatVideoWndClass", "ZPFloatToolbarClass"}

// Apps lists the class apps. When two show a meeting the first wins; she attends one class at a time.
var Apps = []AppSpec{
	{
		App: Zoom, Exe: "Zoom.exe", WindowClass: zoomMeetingClass, SceneName: "classrec - Zoom",
		Matches: func(w Window) bool { return slices.Contains(zoomInCallClasses, w.Class) },
	},
	{
		App: Meet, Exe: "msedge.exe", WindowClass: "Chrome_WidgetWin_1", SceneName: "classrec - Meet",
		Matches: func(w Window) bool { return slices.ContainsFunc(w.Tabs, inMeetCall) },
	},
}

// SpecFor returns the spec of app; ok is false for an unknown app.
func SpecFor(app App) (spec AppSpec, ok bool) {
	for _, s := range Apps {
		if s.App == app {
			return s, true
		}
	}
	return AppSpec{}, false
}

// Observation is what one Sample says about the class apps.
type Observation struct {
	Meeting map[App]string // title of the window showing each app's call, if she is in one
}

// ClassAppsOnly is s without the windows of any app that is not a class app, so a log of it can
// leave the laptop without showing what else she had open.
func (s Sample) ClassAppsOnly() Sample {
	isClassApp := func(path, separators string) bool {
		for _, spec := range Apps {
			if isExe(path, separators, spec.Exe) {
				return true
			}
		}
		return false
	}
	out := Sample{Time: s.Time, AppAudioLive: s.AppAudioLive}
	for _, w := range s.Windows {
		if isClassApp(w.Exe, `\/`) {
			out.Windows = append(out.Windows, w)
		}
	}
	return out
}

// Classify reduces a Sample to the signals the state machine uses.
func Classify(s Sample) Observation {
	o := Observation{Meeting: map[App]string{}}
	for _, spec := range Apps {
		for _, w := range s.Windows {
			// Only a window on screen counts: Zoom keeps its meeting windows, hidden, in the tray
			// after a meeting ends.
			if w.Visible && isExe(w.Exe, `\/`, spec.Exe) && spec.Matches(w) {
				o.Meeting[spec.App] = w.Title
				break
			}
		}
	}
	return o
}

// InClass reports whether any class app shows a meeting window.
func (o Observation) InClass() bool { return len(o.Meeting) > 0 }

// isExe reports whether path ends in the file name exe, ignoring case. separators are the path separators path uses.
func isExe(path, separators, exe string) bool {
	lastSeparator := strings.LastIndexAny(path, separators)
	fileName := path[lastSeparator+1:]
	return strings.EqualFold(fileName, exe)
}

// inMeetCall reports whether an Edge tab is in a Meet call. A Meet tab is named "Meet - abc-defg-hij"
// in the call and after leaving it alike; only in the call does Edge add "Microphone recording" or
// "Camera recording", and it keeps doing so while she is muted with the camera off (seen 2026-11-27).
func inMeetCall(tab string) bool {
	return strings.HasPrefix(tab, "Meet - ") && strings.Contains(tab, " recording")
}

// Phase is where the state machine is.
type Phase string

// The phases of a class.
const (
	Idle      Phase = "idle"
	Candidate Phase = "candidate"
	Recording Phase = "recording"
	Ending    Phase = "ending"
)

// Config holds the two timings of the state machine.
type Config struct {
	Hold  time.Duration // a start signal must last this long before recording starts
	Grace time.Duration // recording stops only after the class has been gone this long
}

// DefaultConfig starts a class after she has been in a call for 10 s and ends it 10 s after she leaves.
var DefaultConfig = Config{Hold: 10 * time.Second, Grace: 10 * time.Second}

// EventKind says whether recording should start or stop.
type EventKind string

// The two decisions.
const (
	Start EventKind = "start"
	Stop  EventKind = "stop"
)

// Event is a start or stop decision.
type Event struct {
	Kind  EventKind `json:"kind"`
	At    time.Time `json:"at"`
	App   App       `json:"app"`
	Title string    `json:"title"`
}

// Machine is the detection state machine.
//
// A class starts when she has been in a call for Hold and ends when she has been out of it for
// Grace. Being in a call is the app's own sign of it (see Apps), so muting, minimising, sharing her
// screen or looking at another Edge tab do not end it.
type Machine struct {
	cfg   Config
	phase Phase
	since time.Time // when Candidate or Ending began
	app   App
	title string
}

// NewMachine returns an Idle machine with cfg's timings.
func NewMachine(cfg Config) *Machine { return &Machine{cfg: cfg, phase: Idle} }

// Phase is where the machine is.
func (m *Machine) Phase() Phase { return m.phase }

// Class is the app and meeting window of the class being started or recorded.
func (m *Machine) Class() (App, string) { return m.app, m.title }

// Step advances the machine with one observation and returns a start or stop event, if any.
func (m *Machine) Step(t time.Time, o Observation) *Event {
	switch m.phase {
	case Candidate:
		return m.stepCandidate(t, o)
	case Recording:
		return m.stepRecording(t, o)
	case Ending:
		return m.stepEnding(t, o)
	default:
		return m.stepIdle(t, o)
	}
}

func (m *Machine) stepIdle(t time.Time, o Observation) *Event {
	app, title, found := startSignal(o)
	if !found {
		return nil
	}
	m.phase = Candidate
	m.since = t
	m.app = app
	m.title = title
	return nil
}

func (m *Machine) stepCandidate(t time.Time, o Observation) *Event {
	if _, _, found := startSignal(o); !found {
		m.reset()
		return nil
	}
	heldFor := t.Sub(m.since)
	if heldFor < m.cfg.Hold {
		return nil
	}
	m.phase = Recording
	return m.event(Start, t)
}

func (m *Machine) stepRecording(t time.Time, o Observation) *Event {
	if m.classStillOn(o) {
		return nil
	}
	m.phase = Ending
	m.since = t
	return nil
}

func (m *Machine) stepEnding(t time.Time, o Observation) *Event {
	if m.classStillOn(o) {
		m.phase = Recording
		return nil
	}
	if goneFor := t.Sub(m.since); goneFor < m.cfg.Grace {
		return nil
	}
	stop := m.event(Stop, t)
	m.reset()
	return stop
}

func (m *Machine) event(kind EventKind, t time.Time) *Event {
	return &Event{Kind: kind, At: t, App: m.app, Title: m.title}
}

func (m *Machine) reset() {
	m.phase = Idle
	m.since = time.Time{}
	m.app = ""
	m.title = ""
}

// classStillOn reports whether she is still in the class's call. Neither the mic nor the class's
// sound counts: leaving the call (Zoom left in the tray, a Meet tab that says she left) ends the
// class 10 s later.
func (m *Machine) classStillOn(o Observation) bool {
	return o.Meeting[m.app] != ""
}

// startSignal is the first app, in Apps order, whose call she is in.
func startSignal(o Observation) (app App, title string, found bool) {
	for _, spec := range Apps {
		if title := o.Meeting[spec.App]; title != "" {
			return spec.App, title, true
		}
	}
	return "", "", false
}

// replay runs a fresh machine over samples and returns the events it produced.
func replay(samples []Sample, cfg Config) []Event {
	m := NewMachine(cfg)
	var events []Event
	for _, s := range samples {
		e := m.Step(s.Time, Classify(s))
		if e != nil {
			events = append(events, *e)
		}
	}
	return events
}

// readJSONL reads samples written by --debug-detect. Extra fields on a line are ignored.
func readJSONL(r io.Reader) ([]Sample, error) {
	const maxLineBytes = 16 * 1024 * 1024
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineBytes)

	var samples []Sample
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := scanner.Bytes()
		if strings.TrimSpace(string(line)) == "" {
			continue
		}
		var s Sample
		if err := json.Unmarshal(line, &s); err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNumber, err)
		}
		samples = append(samples, s)
	}
	return samples, scanner.Err()
}
