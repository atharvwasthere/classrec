package obs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteProfile(t *testing.T) {
	profiles := t.TempDir()
	dir, err := WriteProfile(profiles, ProfileSettings{
		Name: "classrec", RecordDir: `C:\Users\Admin\Class Recordings\_staging`, Encoder: "obs_qsv11_v2",
		BaseWidth: 1920, BaseHeight: 1080, OutputWidth: 1280, OutputHeight: 720, FPS: 15, BitrateKbps: 1000, KeyframeSecs: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	ini, _ := os.ReadFile(filepath.Join(dir, "basic.ini"))
	for _, want := range []string{
		"Mode=Advanced", "RecFormat2=mkv", "RecEncoder=obs_qsv11_v2", `RecFilePath=C:\\Users\\Admin\\Class Recordings\\_staging`,
		"BaseCX=1920", "OutputCX=1280", "OutputCY=720", "FPSNum=15", "FilenameFormatting=%CCYY-%MM-%DD %hh-%mm-%ss",
	} {
		if !strings.Contains(string(ini), want) {
			t.Errorf("basic.ini lacks %q:\n%s", want, ini)
		}
	}
	enc, err := ReadEncoderSettings(dir)
	if err != nil || enc != (EncoderSettings{RateControl: "CBR", Bitrate: 1000, KeyintSec: 2}) {
		t.Fatalf("encoder %+v, err %v", enc, err)
	}
	if ProfileFolder("classrec-t1") != "classrect1" {
		t.Fatal("profile folder name does not match OBS's")
	}
}
