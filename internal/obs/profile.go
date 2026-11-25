package obs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"classrec/internal/fsx"
)

// ProfileSettings is how a profile records.
type ProfileSettings struct {
	Name                      string
	RecordDir                 string
	Encoder                   string // e.g. "obs_qsv11_v2", QuickSync H.264 in OBS 33
	BaseWidth, BaseHeight     int    // canvas; the primary display in true pixels
	OutputWidth, OutputHeight int
	FPS                       int
	BitrateKbps               int // constant bitrate
	KeyframeSecs              int
}

// ProfileFolder is the folder OBS keeps a profile in: it keeps only letters, digits, spaces and
// underscores of the name ("classrec-t1" lives in "classrect1").
func ProfileFolder(name string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ' || r == '_' {
			return r
		}
		return -1
	}, name)
}

// WriteProfile writes a profile's basic.ini and recordEncoder.json into profilesDir
// (…\obs-studio\basic\profiles), replacing what is there. OBS must be closed: it lists profiles
// when it starts and rewrites the current profile's files when it exits. It returns the profile's folder.
func WriteProfile(profilesDir string, s ProfileSettings) (string, error) {
	dir := filepath.Join(profilesDir, ProfileFolder(s.Name))
	iniPath := strings.ReplaceAll(s.RecordDir, `\`, `\\`) // OBS escapes backslashes in its ini files
	ini := fmt.Sprintf(`[General]
Name=%s

[Output]
Mode=Advanced
FilenameFormatting=%%CCYY-%%MM-%%DD %%hh-%%mm-%%ss

[AdvOut]
RecType=Standard
RecFilePath=%s
RecFormat2=mkv
RecEncoder=%s
RecUseRescale=false
RecTracks=1

[Video]
BaseCX=%d
BaseCY=%d
OutputCX=%d
OutputCY=%d
FPSType=2
FPSNum=%d
FPSDen=1
`, s.Name, iniPath, s.Encoder, s.BaseWidth, s.BaseHeight, s.OutputWidth, s.OutputHeight, s.FPS)
	if err := fsx.WriteFileAtomic(filepath.Join(dir, "basic.ini"), []byte(strings.ReplaceAll(ini, "\n", "\r\n"))); err != nil {
		return "", err
	}
	encoder, err := json.MarshalIndent(EncoderSettings{RateControl: "CBR", Bitrate: s.BitrateKbps, KeyintSec: s.KeyframeSecs}, "", "  ")
	if err != nil {
		return "", err
	}
	return dir, fsx.WriteFileAtomic(filepath.Join(dir, "recordEncoder.json"), encoder)
}

// EncoderSettings is the part of recordEncoder.json classrec sets. The websocket cannot reach it.
type EncoderSettings struct {
	RateControl string `json:"rate_control"`
	Bitrate     int    `json:"bitrate"`
	KeyintSec   int    `json:"keyint_sec"`
}

// ReadEncoderSettings reads a profile folder's recordEncoder.json.
func ReadEncoderSettings(profileDir string) (EncoderSettings, error) {
	var e EncoderSettings
	content, err := os.ReadFile(filepath.Join(profileDir, "recordEncoder.json"))
	if err != nil {
		return e, err
	}
	return e, json.Unmarshal(content, &e)
}
