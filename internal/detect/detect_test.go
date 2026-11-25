package detect

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

const (
	edgeExe  = `C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`
	zoomExe  = `C:\Users\Admin\AppData\Roaming\Zoom\bin\Zoom.exe`
	edgeMic  = `C:#Program Files (x86)#Microsoft#Edge#Application#msedge.exe`
	zoomMic  = `C:#Users#Admin#AppData#Roaming#Zoom#bin#Zoom.exe`
	meetName = "Meet - xyz-abcd-efg - Personal - Microsoft Edge"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		s    Sample
		want Observation
	}{
		{"meet tab", Sample{Windows: []Window{{Exe: edgeExe, Title: meetName}}}, Observation{MeetWindow: meetName}},
		{"meeting is not meet", Sample{Windows: []Window{{Exe: edgeExe, Title: "Meeting notes - Microsoft Edge"}}}, Observation{}},
		{"meet in another browser", Sample{Windows: []Window{{Exe: `C:\chrome.exe`, Title: meetName}}}, Observation{}},
		{"zoom meeting by class", Sample{Windows: []Window{{Exe: zoomExe, Class: "ConfMultiTabContentWndClass", Title: "Zoom"}}}, Observation{ZoomWindow: "Zoom"}},
		{"zoom home is not a meeting", Sample{Windows: []Window{{Exe: zoomExe, Class: "ZPPTMainFrmWndClassEx", Title: "Zoom Workplace"}}}, Observation{}},
		{"mic open", Sample{Mic: []MicUse{{Path: zoomMic, Start: 5}, {Path: edgeMic, Start: 5}}}, Observation{ZoomMic: true, EdgeMic: true}},
		{"mic closed", Sample{Mic: []MicUse{{Path: zoomMic, Start: 5, Stop: 6}, {Path: edgeMic}}}, Observation{}},
	}
	for _, tt := range tests {
		if got := Classify(tt.s); got != tt.want {
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
	m := Machine{Cfg: DefaultConfig}
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
	meet := Observation{MeetWindow: "Meet", EdgeMic: true}
	zoom := Observation{ZoomWindow: "Zoom Meeting", ZoomMic: true}
	none := Observation{}
	tests := []struct {
		name  string
		steps []step
		want  []Event
	}{
		{"blip shorter than hold", []step{{1, meet}, {30, none}}, nil},
		{"edge mic alone never starts", []step{{60, Observation{EdgeMic: true}}}, nil},
		{"zoom muted keeps recording", []step{{2, zoom}, {100, Observation{ZoomWindow: "Zoom Meeting"}}, {31, none}},
			[]Event{{"start", at(10), Zoom, "Zoom Meeting"}, {"stop", at(1320), Zoom, "Zoom Meeting"}}},
		{"rejoin within grace is one class", []step{{10, meet}, {17, none}, {10, meet}, {31, none}},
			[]Event{{"start", at(10), Meet, "Meet"}, {"stop", at(670), Meet, "Meet"}}},
		{"rejoin after grace is two classes", []step{{10, meet}, {31, none}, {10, meet}, {31, none}},
			[]Event{{"start", at(10), Meet, "Meet"}, {"stop", at(400), Meet, "Meet"}, {"start", at(420), Meet, "Meet"}, {"stop", at(810), Meet, "Meet"}}},
		{"edge mic does not sustain a zoom class", []step{{10, zoom}, {31, Observation{EdgeMic: true}}},
			[]Event{{"start", at(10), Zoom, "Zoom Meeting"}, {"stop", at(400), Zoom, "Zoom Meeting"}}},
		{"audio alone does not start a class", []step{{60, Observation{AppAudio: true}}}, nil},
		{"audio keeps a class whose window is gone and mic is off", []step{{10, meet}, {60, Observation{AppAudio: true}}, {31, none}},
			[]Event{{"start", at(10), Meet, "Meet"}, {"stop", at(1000), Meet, "Meet"}}},
		{"audio blip inside the grace restarts it", []step{{10, meet}, {20, none}, {1, Observation{AppAudio: true}}, {31, none}},
			[]Event{{"start", at(10), Meet, "Meet"}, {"stop", at(610), Meet, "Meet"}}},
	}
	for _, tt := range tests {
		if got := run(tt.steps...); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", tt.name, got, tt.want)
		}
	}
}

// TestReplayKeepsClassWhileAppAudioIsLive checks the audio signal survives the --debug-detect JSONL
// round trip and decides the stop time: the Meet tab is closed and the mic is off, yet the teacher
// is still audible for 10 more minutes.
func TestReplayKeepsClassWhileAppAudioIsLive(t *testing.T) {
	var log bytes.Buffer
	enc := json.NewEncoder(&log)
	meetTab := []Window{{Exe: edgeExe, Title: meetName, Visible: true}}
	for i := 0; i < 101; i++ {
		s := Sample{Time: t0.Add(time.Duration(i) * 10 * time.Second)}
		switch {
		case i < 10: // joining
			s.Windows = meetTab
		case i < 70: // tab closed, still listening
			s.AppAudioLive = i < 70
		}
		enc.Encode(s)
	}
	samples, err := ReadJSONL(&log)
	if err != nil {
		t.Fatal(err)
	}
	got := Replay(samples, DefaultConfig)
	want := []Event{{"start", at(10), Meet, meetName}, {"stop", at(1000), Meet, meetName}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

// TestReplay runs the rules over recorded logs. Real --debug-detect logs go in testdata with their expected events.
func TestReplay(t *testing.T) {
	ist := time.FixedZone("IST", 5*3600+1800)
	d := func(h, m, s int) time.Time { return time.Date(2026, 11, 20, h, m, s, 0, ist) }
	tests := []struct {
		file string
		want []Event
	}{
		// synthetic: Meet from 18:01, tab switch with mic on 18:20, muted and away 18:25-18:27, leaves 18:50
		{"synthetic_meet_tabswitch_mute.jsonl", []Event{
			{"start", d(18, 1, 10), Meet, meetName},
			{"stop", d(18, 55, 0), Meet, meetName},
		}},
	}
	for _, tt := range tests {
		f, err := os.Open(filepath.Join("testdata", tt.file))
		if err != nil {
			t.Fatal(err)
		}
		samples, err := ReadJSONL(f)
		f.Close()
		if err != nil {
			t.Fatalf("%s: %v", tt.file, err)
		}
		got := Replay(samples, DefaultConfig)
		if len(got) != len(tt.want) {
			t.Fatalf("%s: got %+v, want %+v", tt.file, got, tt.want)
		}
		for i := range got {
			if got[i].Kind != tt.want[i].Kind || !got[i].At.Equal(tt.want[i].At) || got[i].App != tt.want[i].App || got[i].Title != tt.want[i].Title {
				t.Errorf("%s event %d: got %+v, want %+v", tt.file, i, got[i], tt.want[i])
			}
		}
	}
}
