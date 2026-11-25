package recorder

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"classrec/internal/fsx"
)

// What every recording uses, whatever she has set up in OBS by hand.
const (
	DefaultProfileName = "classrec"
	// DefaultEncoder is OBS 33's QuickSync H.264. Older OBS called it obs_qsv11.
	DefaultEncoder = "obs_qsv11_v2"

	outputWidth  = 1280
	outputHeight = 720
	outputFPS    = 15
	bitrateKbps  = 1000
	keyframeSecs = 2
)

// param is one profile.ini value.
type param struct{ Category, Name, Value string }

func profileParams(encoder string) []param {
	return []param{
		{"Output", "Mode", "Advanced"},
		{"AdvOut", "RecType", "Standard"},
		{"AdvOut", "RecFormat2", "mkv"},
		{"AdvOut", "RecEncoder", encoder},
		{"AdvOut", "RecRescale", "false"},
		{"AdvOut", "RecTracks", "1"},
	}
}

// ApplyProfile makes OBS use the classrec profile with the recording settings above. It returns a
// description of every setting OBS would not take, or took but reads back differently; those are
// not errors, since recording with a slightly different setting beats not recording.
func (r *Recorder) ApplyProfile(ctx context.Context) (problems []string, err error) {
	c := r.client
	names, current, err := c.Profiles(ctx)
	if err != nil {
		return nil, err
	}
	switch {
	case !contains(names, r.cfg.ProfileName):
		err = c.CreateProfile(ctx, r.cfg.ProfileName) // OBS switches to it
	case current != r.cfg.ProfileName:
		err = c.SetCurrentProfile(ctx, r.cfg.ProfileName)
	}
	if err != nil {
		return nil, fmt.Errorf("selecting the %s profile: %w", r.cfg.ProfileName, err)
	}

	for _, p := range profileParams(r.cfg.Encoder) {
		if err := c.SetProfileParameter(ctx, p.Category, p.Name, p.Value); err != nil {
			problems = append(problems, fmt.Sprintf("%s/%s: obs would not set it: %v", p.Category, p.Name, err))
			continue
		}
		got, err := c.ProfileParameter(ctx, p.Category, p.Name)
		if err != nil || got != p.Value {
			problems = append(problems, fmt.Sprintf("%s/%s: wanted %q, obs has %q", p.Category, p.Name, p.Value, got))
		}
	}

	problems = append(problems, r.applyVideo(ctx)...)

	changed, err := r.writeEncoderSettings()
	if err != nil {
		problems = append(problems, "recordEncoder.json: "+err.Error())
	}
	if changed {
		// OBS reads recordEncoder.json when it builds its outputs, which happens on a profile switch.
		for _, other := range names {
			if other != r.cfg.ProfileName {
				if err := c.SetCurrentProfile(ctx, other); err == nil {
					err = c.SetCurrentProfile(ctx, r.cfg.ProfileName)
				}
				if err != nil {
					return problems, fmt.Errorf("reloading the %s profile: %w", r.cfg.ProfileName, err)
				}
				break
			}
		}
	}
	return problems, nil
}

func (r *Recorder) applyVideo(ctx context.Context) []string {
	want := videoSettings()
	if err := r.client.SetVideoSettings(ctx, want); err != nil {
		return []string{"video 1280x720 at 15 fps: obs would not set it: " + err.Error()}
	}
	got, err := r.client.VideoSettings(ctx)
	if err != nil || got.OutputWidth != want.OutputWidth || got.OutputHeight != want.OutputHeight ||
		got.FPSNumerator != want.FPSNumerator || got.FPSDenominator != want.FPSDenominator {
		return []string{fmt.Sprintf("video: wanted %dx%d at %d fps, obs has %+v", want.OutputWidth, want.OutputHeight, outputFPS, got)}
	}
	return nil
}

// writeEncoderSettings sets CBR, the bitrate and the keyframe interval in the profile's
// recordEncoder.json, which is where OBS keeps encoder settings; the websocket cannot reach them.
// It keeps any other keys already there and reports whether the file changed.
func (r *Recorder) writeEncoderSettings() (changed bool, err error) {
	if r.cfg.ProfileDir == "" {
		return false, nil
	}
	path := filepath.Join(r.cfg.ProfileDir, "recordEncoder.json")
	settings := map[string]any{}
	before, readErr := os.ReadFile(path)
	if readErr == nil {
		if err := json.Unmarshal(before, &settings); err != nil {
			settings = map[string]any{} // unreadable: replace, it is ours to own
		}
	}
	settings["rate_control"] = "CBR"
	settings["bitrate"] = bitrateKbps
	settings["keyint_sec"] = keyframeSecs
	after, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return false, err
	}
	if readErr == nil && bytes.Equal(bytes.TrimSpace(before), after) {
		return false, nil
	}
	return true, fsx.WriteFileAtomic(path, after)
}

func videoSettings() (v videoSettingsT) {
	return videoSettingsT{
		FPSNumerator: outputFPS, FPSDenominator: 1,
		BaseWidth: outputWidth, BaseHeight: outputHeight,
		OutputWidth: outputWidth, OutputHeight: outputHeight,
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
