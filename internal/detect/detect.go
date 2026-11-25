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
func (m MicUse) InUse() bool { return m.Stop == 0 && m.Start != 0 }

const (
	Zoom = "Zoom"
	Meet = "Meet"
)

// Observation is what one Sample says about Zoom and Meet.
type Observation struct {
	ZoomWindow string // title of a Zoom meeting window, "" if none
	MeetWindow string // title of an Edge window showing Meet, "" if none
	ZoomMic    bool
	EdgeMic    bool
}

// Classify reduces a Sample to the signals the state machine uses.
func Classify(s Sample) Observation {
	var o Observation
	for _, w := range s.Windows {
		switch exe := baseName(w.Exe, `\/`); {
		case strings.EqualFold(exe, "zoom.exe") && o.ZoomWindow == "" && isZoomMeeting(w):
			o.ZoomWindow = w.Title
		case strings.EqualFold(exe, "msedge.exe") && o.MeetWindow == "" && hasWord(w.Title, "Meet"):
			o.MeetWindow = w.Title
		}
	}
	for _, m := range s.Mic {
		if !m.InUse() {
			continue
		}
		switch exe := baseName(m.Path, "#"); {
		case strings.EqualFold(exe, "zoom.exe"):
			o.ZoomMic = true
		case strings.EqualFold(exe, "msedge.exe"):
			o.EdgeMic = true
		}
	}
	return o
}

// isZoomMeeting matches the meeting window by class or title. UNVERIFIED: both come from
// Zoom 5.x/6.x reports and must be checked against a real --debug-detect log.
func isZoomMeeting(w Window) bool {
	return w.Class == "ConfMultiTabContentWndClass" || strings.Contains(w.Title, "Zoom Meeting")
}

// hasWord matches word as a whole word, so "Meet - abc" matches and "Meeting notes" does not.
func hasWord(s, word string) bool {
	for i := 0; ; {
		j := strings.Index(s[i:], word)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(word)
		before, _ := utf8.DecodeLastRuneInString(s[:start])
		after, _ := utf8.DecodeRuneInString(s[end:])
		if !unicode.IsLetter(before) && !unicode.IsLetter(after) {
			return true
		}
		i = end
	}
}

func baseName(path, seps string) string {
	return path[strings.LastIndexAny(path, seps)+1:]
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

var DefaultConfig = Config{Hold: 10 * time.Second, Grace: 3 * time.Minute}

// Event is a start or stop decision.
type Event struct {
	Kind  string    `json:"kind"` // "start" or "stop"
	At    time.Time `json:"at"`
	App   string    `json:"app"` // Zoom or Meet
	Title string    `json:"title"`
}

// Machine is the detection state machine. The zero value is Idle.
type Machine struct {
	Cfg   Config
	Phase Phase
	Since time.Time // when Candidate or Ending began
	App   string
	Title string
}

// Step advances the machine with one observation and returns a start or stop event, if any.
// A class can only start from a meeting window, because Edge opens the mic for other sites too.
// Once recording, the app's mic alone keeps it going, so a tab switch in Edge doesn't end it,
// and the window alone keeps it going, so muting doesn't end it.
func (m *Machine) Step(t time.Time, o Observation) *Event {
	app, title := startSignal(o)
	switch m.Phase {
	case "", Idle:
		if app != "" {
			m.Phase, m.Since, m.App, m.Title = Candidate, t, app, title
		}
	case Candidate:
		if app == "" {
			m.reset()
			return nil
		}
		if t.Sub(m.Since) >= m.Cfg.Hold {
			m.Phase = Recording
			return &Event{Kind: "start", At: t, App: m.App, Title: m.Title}
		}
	case Recording:
		if !m.sustained(o) {
			m.Phase, m.Since = Ending, t
		}
	case Ending:
		if m.sustained(o) {
			m.Phase = Recording
			return nil
		}
		if t.Sub(m.Since) >= m.Cfg.Grace {
			e := &Event{Kind: "stop", At: t, App: m.App, Title: m.Title}
			m.reset()
			return e
		}
	}
	return nil
}

func (m *Machine) reset() { m.Phase, m.Since, m.App, m.Title = Idle, time.Time{}, "", "" }

func (m *Machine) sustained(o Observation) bool {
	if m.App == Zoom {
		return o.ZoomWindow != "" || o.ZoomMic
	}
	return o.MeetWindow != "" || o.EdgeMic
}

// startSignal prefers Zoom when both are open; she attends one class at a time.
func startSignal(o Observation) (app, title string) {
	switch {
	case o.ZoomWindow != "":
		return Zoom, o.ZoomWindow
	case o.MeetWindow != "":
		return Meet, o.MeetWindow
	}
	return "", ""
}

// Replay runs a fresh machine over samples and returns the events it produced.
func Replay(samples []Sample, cfg Config) []Event {
	m := Machine{Cfg: cfg}
	var events []Event
	for _, s := range samples {
		if e := m.Step(s.Time, Classify(s)); e != nil {
			events = append(events, *e)
		}
	}
	return events
}

// ReadJSONL reads samples written by --debug-detect. Extra fields on a line are ignored.
func ReadJSONL(r io.Reader) ([]Sample, error) {
	var samples []Sample
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for n := 1; sc.Scan(); n++ {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		var s Sample
		if err := json.Unmarshal(sc.Bytes(), &s); err != nil {
			return nil, fmt.Errorf("line %d: %w", n, err)
		}
		samples = append(samples, s)
	}
	return samples, sc.Err()
}
