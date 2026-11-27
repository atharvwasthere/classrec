package detect

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

const (
	edgeExe  = `C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`
	zoomExe  = `C:\Users\Admin\AppData\Roaming\Zoom\bin\Zoom.exe`
	meetName = "Meet - xyz-abcd-efg - Personal - Microsoft Edge"
)

// seen builds an Observation from the title of the window showing each app's call.
func seen(meeting map[App]string) Observation {
	o := Observation{Meeting: map[App]string{}}
	for app, title := range meeting {
		o.Meeting[app] = title
	}
	return o
}

func TestClassify(t *testing.T) {
	// The Edge window with Meet in it; its tab names are as Edge gave them on 2026-11-27.
	inCall := Window{Exe: edgeExe, Class: "Chrome_WidgetWin_1", Title: meetName, Visible: true,
		Tabs: []string{"YouTube", "Meet - xyz-abcd-efg - Microphone recording - Memory usage - 439 MB"}}
	left := inCall
	left.Tabs = []string{"Meet - xyz-abcd-efg - Memory usage - 475 MB"}
	onOtherTab := inCall
	onOtherTab.Title = "YouTube - Personal - Microsoft Edge"
	tests := []struct {
		name string
		s    Sample
		want Observation
	}{
		{"in a meet call", Sample{Windows: []Window{inCall}}, seen(map[App]string{Meet: meetName})},
		{"in a meet call, looking at another tab", Sample{Windows: []Window{onOtherTab}}, seen(map[App]string{Meet: onOtherTab.Title})},
		{"left the meet call, tab still open", Sample{Windows: []Window{left}}, seen(nil)},
		{"meet tab in another browser", Sample{Windows: []Window{{Exe: `C:\chrome.exe`, Title: meetName, Visible: true, Tabs: inCall.Tabs}}}, seen(nil)},
		{"zoom meeting by class", Sample{Windows: []Window{{Exe: zoomExe, Class: "ConfMultiTabContentWndClass", Title: "Zoom Meeting", Visible: true}}}, seen(map[App]string{Zoom: "Zoom Meeting"})},
		// After a meeting Zoom stays in the tray and keeps its meeting window, hidden (seen 2026-11-26).
		{"zoom left in the tray is not a meeting", Sample{Windows: []Window{
			{Exe: zoomExe, Class: "ConfMultiTabContentWndClass", Title: "Zoom Workplace"},
			inCall,
		}}, seen(map[App]string{Meet: meetName})},
		{"zoom home is not a meeting", Sample{Windows: []Window{{Exe: zoomExe, Class: "ZPPTMainFrmWndClassEx", Title: "Zoom Workplace", Visible: true}}}, seen(nil)},
		// Minimised, Zoom swaps its meeting window for a floating video window; sharing their screen, it
		// adds a toolbar (seen 2026-11-27).
		{"zoom minimised", Sample{Windows: []Window{
			{Exe: zoomExe, Class: "ConfMultiTabContentWndClass", Title: "Zoom Meeting"},
			{Exe: zoomExe, Class: "ZPFloatVideoWndClass", Title: "Zoom Workplace", Visible: true},
		}}, seen(map[App]string{Zoom: "Zoom Workplace"})},
		{"zoom sharing their screen", Sample{Windows: []Window{
			{Exe: zoomExe, Class: "ConfMultiTabContentWndClass", Title: "Zoom Meeting"},
			{Exe: zoomExe, Class: "ZPFloatToolbarClass", Title: "Screen sharing meeting controls", Visible: true},
		}}, seen(map[App]string{Zoom: "Screen sharing meeting controls"})},
		{"zoom left, its windows hidden", Sample{Windows: []Window{
			{Exe: zoomExe, Class: "ConfMultiTabContentWndClass", Title: "Zoom Workplace"},
			{Exe: zoomExe, Class: "ZPFloatVideoWndClass", Title: "Zoom Workplace"},
		}}, seen(nil)},
	}
	for _, tt := range tests {
		if got := Classify(tt.s); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: got %+v, want %+v", tt.name, got, tt.want)
		}
	}
}

var t0 = time.Date(2026, 11, 20, 18, 0, 0, 0, time.UTC)

// run feeds one observation every 10 s; each step repeats an observation for n samples.
type step struct {
	n int
	o Observation
}

func run(steps ...step) []Event {
	m := NewMachine(DefaultConfig)
	var events []Event
	i := 0
	for _, s := range steps {
		for range s.n {
			if e := m.Step(t0.Add(time.Duration(i)*10*time.Second), s.o); e != nil {
				events = append(events, *e)
			}
			i++
		}
	}
	return events
}

func at(sec int) time.Time { return t0.Add(time.Duration(sec) * time.Second) }

func TestMachine(t *testing.T) {
	meet := seen(map[App]string{Meet: "Meet"})
	zoom := seen(map[App]string{Zoom: "Zoom Meeting"})
	none := seen(nil)
	tests := []struct {
		name  string
		steps []step
		want  []Event
	}{
		{"blip shorter than hold", []step{{1, meet}, {30, none}}, nil},
		{"leaving stops 10 s later", []step{{10, meet}, {3, none}},
			[]Event{{"start", at(10), Meet, "Meet"}, {"stop", at(110), Meet, "Meet"}}},
		{"a long class is one recording", []step{{2, zoom}, {100, zoom}, {3, none}},
			[]Event{{"start", at(10), Zoom, "Zoom Meeting"}, {"stop", at(1030), Zoom, "Zoom Meeting"}}},
		{"leaving zoom for meet ends zoom and starts meet", []step{{10, zoom}, {4, meet}},
			[]Event{{"start", at(10), Zoom, "Zoom Meeting"}, {"stop", at(110), Zoom, "Zoom Meeting"}, {"start", at(130), Meet, "Meet"}}},
	}
	for _, tt := range tests {
		if got := run(tt.steps...); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", tt.name, got, tt.want)
		}
	}
}

// TestReplayIgnoresClassSound checks, through the --debug-detect JSONL round trip, that the class's
// sound does not keep a recording going: the Meet tab is closed and the mic is off while OBS still
// hears audio, and the class ends 10 s later.
func TestReplayIgnoresClassSound(t *testing.T) {
	var log bytes.Buffer
	enc := json.NewEncoder(&log)
	meetTab := []Window{{Exe: edgeExe, Class: "Chrome_WidgetWin_1", Title: meetName, Visible: true,
		Tabs: []string{"Meet - xyz-abcd-efg - Microphone recording"}}}
	for i := 0; i < 30; i++ {
		s := Sample{Time: t0.Add(time.Duration(i) * 10 * time.Second), AppAudioLive: true}
		if i < 10 { // in the call; then the tab is closed
			s.Windows = meetTab
		}
		enc.Encode(s)
	}
	samples, err := readJSONL(&log)
	if err != nil {
		t.Fatal(err)
	}
	got := replay(samples, DefaultConfig)
	want := []Event{{"start", at(10), Meet, meetName}, {"stop", at(110), Meet, meetName}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

// Promise: the detect log that leaves the laptop holds only Zoom and Edge, never their other windows.
func TestClassAppsOnlyStripsOtherApps(t *testing.T) {
	s := Sample{
		Windows: []Window{
			{Exe: edgeExe, Title: meetName},
			{Exe: `C:\Program Files\WhatsApp\WhatsApp.exe`, Title: "WhatsApp - private chat"},
			{Exe: zoomExe, Title: "Zoom Meeting"},
		},
	}
	got := s.ClassAppsOnly()
	if len(got.Windows) != 2 || got.Windows[0].Exe != edgeExe || got.Windows[1].Exe != zoomExe {
		t.Errorf("windows %+v", got.Windows)
	}
}

// Promise: a new machine is Idle, so the agent's between-classes work (updates) runs from the start.
func TestNewMachineIsIdle(t *testing.T) {
	if p := NewMachine(DefaultConfig).Phase(); p != Idle {
		t.Fatalf("phase %q, want %q", p, Idle)
	}
}
