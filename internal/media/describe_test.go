package media

import "testing"

func TestParseDescribe(t *testing.T) {
	raw := []byte(`{
	  "streams": [
	    {"codec_type":"video","codec_name":"h264","profile":"High","width":1280,"height":720,"avg_frame_rate":"15/1"},
	    {"codec_type":"audio","codec_name":"aac","sample_rate":"48000","channels":2}
	  ],
	  "format": {"duration":"30.5","size":"3812500"}
	}`)
	info, err := parseDescribe(raw)
	if err != nil {
		t.Fatal(err)
	}
	if info.Video == nil || info.Video.Width != 1280 || info.Video.Height != 720 || info.Video.FPS != 15 || info.Video.Codec != "h264" {
		t.Fatalf("video %+v", info.Video)
	}
	if len(info.Audio) != 1 || info.Audio[0].SampleRate != 48000 || info.Audio[0].Channels != 2 {
		t.Fatalf("audio %+v", info.Audio)
	}
	if info.BitRate != 1000000 { // 3812500 B * 8 / 30.5 s
		t.Fatalf("bit rate %d: the container gave none, so it must come from size and duration", info.BitRate)
	}
}

func TestParseRate(t *testing.T) {
	for in, want := range map[string]float64{"15/1": 15, "30000/1001": 29.97002997002997, "0/0": 0, "": 0, "25": 25} {
		if got := parseRate(in); got != want {
			t.Errorf("parseRate(%q) = %v, want %v", in, got, want)
		}
	}
}
