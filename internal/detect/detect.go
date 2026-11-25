// Package detect decides from window and microphone samples when a class starts and ends.
// Everything here is pure, so recorded --debug-detect logs replay in tests.
package detect

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Sample is one observation of the desktop, as written to the --debug-detect JSONL log.
type Sample struct {
	Time    time.Time `json:"t"`
	Windows []Window  `json:"windows"`
	Mic     []MicUse  `json:"mic"`
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
}

// MicUse is one CapabilityAccessManager microphone entry; Path uses # as the separator, as the registry does.
type MicUse struct {
	Path  string `json:"path"`
	Start int64  `json:"start"` // LastUsedTimeStart, FILETIME
	Stop  int64  `json:"stop"`  // LastUsedTimeStop, FILETIME; 0 while the mic is open
}

// InUse reports whether the app has the mic open right now.
func (m MicUse) InUse() bool {
	everUsed := m.Start != 0
	notStopped := m.Stop == 0
	return everUsed && notStopped
}

// The two class apps.
const (
	Zoom = "Zoom"
	Meet = "Meet"
)

const (
	zoomExeName = "zoom.exe"
	edgeExeName = "msedge.exe"

	// UNVERIFIED: the Zoom meeting window's class and title come from Zoom 5.x/6.x reports,
	// to be checked against a real --debug-detect log.
	zoomMeetingClass = "ConfMultiTabContentWndClass"
	zoomMeetingTitle = "Zoom Meeting"
	meetTitleWord    = "Meet"
)

// Observation is what one Sample says about Zoom and Meet.
type Observation struct {
	ZoomWindow string // title of a Zoom meeting window, "" if none
	MeetWindow string // title of an Edge window showing Meet, "" if none
	ZoomMic    bool
	EdgeMic    bool
	AppAudio   bool // the class app's audio is audible in OBS
}

// Classify reduces a Sample to the signals the state machine uses.
func Classify(s Sample) Observation {
	o := Observation{AppAudio: s.AppAudioLive}
	for _, w := range s.Windows {
		if o.ZoomWindow == "" && isZoomMeetingWindow(w) {
			o.ZoomWindow = w.Title
		}
		if o.MeetWindow == "" && isMeetWindow(w) {
			o.MeetWindow = w.Title
		}
	}
	for _, m := range s.Mic {
		if isMicInUseBy(m, zoomExeName) {
			o.ZoomMic = true
		}
		if isMicInUseBy(m, edgeExeName) {
			o.EdgeMic = true
		}
	}
	return o
}

func isZoomMeetingWindow(w Window) bool {
	if !isExe(w.Exe, `\/`, zoomExeName) {
		return false
	}
	hasMeetingClass := w.Class == zoomMeetingClass
	hasMeetingTitle := strings.Contains(w.Title, zoomMeetingTitle)
	return hasMeetingClass || hasMeetingTitle
}

func isMeetWindow(w Window) bool {
	if !isExe(w.Exe, `\/`, edgeExeName) {
		return false
	}
	return hasWord(w.Title, meetTitleWord)
}

func isMicInUseBy(m MicUse, exe string) bool {
	if !m.InUse() {
		return false
	}
	return isExe(m.Path, "#", exe)
}

// isExe reports whether path ends in the file name exe, ignoring case. separators are the path separators path uses.
func isExe(path, separators, exe string) bool {
	lastSeparator := strings.LastIndexAny(path, separators)
	fileName := path[lastSeparator+1:]
	return strings.EqualFold(fileName, exe)
}

// hasWord matches word as a whole word, so "Meet - abc" matches and "Meeting notes" does not.
func hasWord(text, word string) bool {
	searchFrom := 0
	for {
		found := strings.Index(text[searchFrom:], word)
		if found < 0 {
			return false
		}
		start := searchFrom + found
		end := start + len(word)
		if !letterBefore(text, start) && !letterAfter(text, end) {
			return true
		}
		searchFrom = end
	}
}

func letterBefore(text string, i int) bool {
	r, _ := utf8.DecodeLastRuneInString(text[:i])
	return unicode.IsLetter(r)
}

func letterAfter(text string, i int) bool {
	r, _ := utf8.DecodeRuneInString(text[i:])
	return unicode.IsLetter(r)
}

// Phase is where the state machine is.
type Phase string

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

var DefaultConfig = Config{Hold: 10 * time.Second, Grace: 5 * time.Minute}

// EventKind says whether recording should start or stop.
type EventKind string

const (
	Start EventKind = "start"
	Stop  EventKind = "stop"
)

// Event is a start or stop decision.
type Event struct {
	Kind  EventKind `json:"kind"`
	At    time.Time `json:"at"`
	App   string    `json:"app"` // Zoom or Meet
	Title string    `json:"title"`
}

// classSignal is a meeting window that can start a class.
type classSignal struct {
	App   string
	Title string
}

func (s classSignal) present() bool { return s.App != "" }

// Machine is the detection state machine. The zero value is Idle.
//
// A class can only start from a meeting window, because Edge opens the mic for other sites too.
// Once recording, the app's mic alone keeps it going, so a tab switch in Edge doesn't end it,
// and the window alone keeps it going, so muting doesn't end it.
type Machine struct {
	Cfg   Config
	Phase Phase
	Since time.Time // when Candidate or Ending began
	App   string
	Title string
}

// Step advances the machine with one observation and returns a start or stop event, if any.
func (m *Machine) Step(t time.Time, o Observation) *Event {
	switch m.Phase {
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
	signal := startSignal(o)
	if !signal.present() {
		return nil
	}
	m.Phase = Candidate
	m.Since = t
	m.App = signal.App
	m.Title = signal.Title
	return nil
}

func (m *Machine) stepCandidate(t time.Time, o Observation) *Event {
	signal := startSignal(o)
	if !signal.present() {
		m.reset()
		return nil
	}
	heldFor := t.Sub(m.Since)
	if heldFor < m.Cfg.Hold {
		return nil
	}
	m.Phase = Recording
	return m.event(Start, t)
}

func (m *Machine) stepRecording(t time.Time, o Observation) *Event {
	if m.classStillOn(o) {
		return nil
	}
	m.Phase = Ending
	m.Since = t
	return nil
}

func (m *Machine) stepEnding(t time.Time, o Observation) *Event {
	if m.classStillOn(o) {
		m.Phase = Recording
		return nil
	}
	goneFor := t.Sub(m.Since)
	if goneFor < m.Cfg.Grace {
		return nil
	}
	stop := m.event(Stop, t)
	m.reset()
	return stop
}

func (m *Machine) event(kind EventKind, t time.Time) *Event {
	return &Event{Kind: kind, At: t, App: m.App, Title: m.Title}
}

func (m *Machine) reset() {
	m.Phase = Idle
	m.Since = time.Time{}
	m.App = ""
	m.Title = ""
}

// classStillOn reports whether the class is still going: the app shows its meeting window, holds the
// mic, or its audio is audible in OBS. Any one is enough, so a closed window with the teacher still
// talking keeps the recording, and so does a muted mic.
func (m *Machine) classStillOn(o Observation) bool {
	if o.AppAudio {
		return true
	}
	if m.App == Zoom {
		return o.ZoomWindow != "" || o.ZoomMic
	}
	return o.MeetWindow != "" || o.EdgeMic
}

// startSignal prefers Zoom when both are open; she attends one class at a time.
func startSignal(o Observation) classSignal {
	if o.ZoomWindow != "" {
		return classSignal{App: Zoom, Title: o.ZoomWindow}
	}
	if o.MeetWindow != "" {
		return classSignal{App: Meet, Title: o.MeetWindow}
	}
	return classSignal{}
}

// Replay runs a fresh machine over samples and returns the events it produced.
func Replay(samples []Sample, cfg Config) []Event {
	m := Machine{Cfg: cfg}
	var events []Event
	for _, s := range samples {
		e := m.Step(s.Time, Classify(s))
		if e != nil {
			events = append(events, *e)
		}
	}
	return events
}

// ReadJSONL reads samples written by --debug-detect. Extra fields on a line are ignored.
func ReadJSONL(r io.Reader) ([]Sample, error) {
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
