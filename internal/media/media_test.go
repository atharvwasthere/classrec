package media

import (
	"reflect"
	"testing"
)

func TestParseSilences(t *testing.T) {
	stderr := `[silencedetect @ 0x1] silence_start: 10.5
[silencedetect @ 0x1] silence_end: 12.25 | silence_duration: 1.75
[silencedetect @ 0x1] silence_start: 30
[silencedetect @ 0x1] silence_end: 30.6 | silence_duration: 0.6
[silencedetect @ 0x1] silence_start: 99.9`
	want := []Silence{{10.5, 12.25}, {30, 30.6}}
	if got := ParseSilences(stderr); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}
