package recorder

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"classrec/internal/fsx"
	"classrec/internal/obs"
)

// What every recording uses, whatever she has set up in OBS by hand.
const (
	DefaultProfileName = "classrec"
	// DefaultEncoder is OBS 33's QuickSync H.264. Older OBS called it obs_qsv11.
	DefaultEncoder = "obs_qsv11_v2"

	outputHeight = 720

	// profileSettle is how long OBS needs to load a profile and rebuild its outputs.
	profileSettle       = 2 * time.Second
	profileWaitEvery    = 250 * time.Millisecond
	profileWaitAttempts = 40
	outputFPS           = 15
	bitrateKbps         = 1000
	keyframeSecs        = 2
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
// not errors, since recording with a slightly different setting beats not recording. err is set
// only when the profile itself could not be selected.
func (r *Recorder) ApplyProfile(ctx context.Context) (problems []string, err error) {
	c := r.currentClient()
	names, current, err := c.Profiles(ctx)
	if err != nil {
		return nil, err
	}
	switch {
	case !contains(names, r.cfg.ProfileName):
		err = c.CreateProfile(ctx, r.cfg.ProfileName) // OBS switches to it
	case current != r.cfg.ProfileName:
		err = c.SetCurrentProfile(ctx, r.cfg.ProfileName)
	default:
		current = "" // nothing to wait for
	}
	if err != nil {
		return nil, fmt.Errorf("selecting the %s profile: %w", r.cfg.ProfileName, err)
	}
	if current != "" || !contains(names, r.cfg.ProfileName) {
		// OBS answers before it has finished switching, and settings sent in between land on the
		// profile it is leaving: a fresh profile then keeps its defaults.
		if err := r.waitForProfile(ctx, r.cfg.ProfileName); err != nil {
			return nil, err
		}
	}

	reload := false
	for _, p := range profileParams(r.cfg.Encoder) {
		before, _ := c.ProfileParameter(ctx, p.Category, p.Name)
		if err := c.SetProfileParameter(ctx, p.Category, p.Name, p.Value); err != nil {
			problems = append(problems, fmt.Sprintf("%s/%s: obs would not set it: %v", p.Category, p.Name, err))
			continue
		}
		got, err := c.ProfileParameter(ctx, p.Category, p.Name)
		if err != nil || got != p.Value {
			problems = append(problems, fmt.Sprintf("%s/%s: wanted %q, obs has %q", p.Category, p.Name, p.Value, got))
		}
		reload = reload || before != p.Value
	}
	encoderChanged, err := r.writeEncoderSettings()
	if err != nil {
		problems = append(problems, "recordEncoder.json: "+err.Error())
	}
	reload = reload || encoderChanged

	if reload {
		// OBS builds its outputs, and so reads the output mode and recordEncoder.json, only when a
		// profile is loaded. Without this a first run records with the old simple-mode settings.
		if err := r.reloadProfile(ctx, names); err != nil {
			problems = append(problems, err.Error())
		}
	}
	// Last, because loading a profile resets the video settings.
	problems = append(problems, r.applyVideo(ctx)...)
	return problems, nil
}

// reloadProfile switches OBS to another profile and back, which makes it rebuild its outputs, then
// waits for it to finish: a recording started too soon still uses the old outputs.
func (r *Recorder) reloadProfile(ctx context.Context, names []string) error {
	c := r.currentClient()
	for _, other := range names {
		if other == r.cfg.ProfileName {
			continue
		}
		for _, name := range []string{other, r.cfg.ProfileName} {
			if err := c.SetCurrentProfile(ctx, name); err != nil {
				return fmt.Errorf("reloading the %s profile: %w", r.cfg.ProfileName, err)
			}
			if err := r.waitForProfile(ctx, name); err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("reloading the %s profile: obs has no other profile to switch to, so the new output settings may not apply until OBS restarts", r.cfg.ProfileName)
}

// waitForProfile waits until OBS reports name as its current profile, then a moment more for it
// to finish loading and rebuild its outputs.
func (r *Recorder) waitForProfile(ctx context.Context, name string) error {
	c := r.currentClient()
	for attempt := 0; attempt < profileWaitAttempts; attempt++ {
		_, current, err := c.Profiles(ctx)
		if err == nil && current == name {
			return r.cfg.Sleep(ctx, profileSettle)
		}
		if err := r.cfg.Sleep(ctx, profileWaitEvery); err != nil {
			return err
		}
	}
	return fmt.Errorf("obs did not switch to the %s profile", name)
}

// outputSize is the recorded size for a canvas: 1280x720 for any 16:9 screen, else 720 high at the
// screen's own shape, so the picture is scaled but never stretched.
func outputSize(baseW, baseH int) (w, h int) {
	const sixteenNine = 16.0 / 9.0
	aspect := float64(baseW) / float64(baseH)
	if math.Abs(aspect-sixteenNine)/sixteenNine < 0.02 {
		return 1280, outputHeight
	}
	w = int(math.Round(aspect*outputHeight/2)) * 2
	return w, outputHeight
}

// applyVideo makes the canvas the primary display, so a window on it is captured at its true size,
// and the output 1280x720, scaled down from the canvas.
func (r *Recorder) applyVideo(ctx context.Context) []string {
	c := r.currentClient()
	baseW, baseH, err := r.primaryDisplay(ctx)
	if err != nil {
		// keep whatever canvas OBS has; the scene item still fits to it below
		current, verr := c.VideoSettings(ctx)
		if verr != nil {
			return []string{"video: no display size and no current settings: " + err.Error()}
		}
		r.canvasW, r.canvasH = current.BaseWidth, current.BaseHeight
		return []string{"video: could not read the display size, left the canvas at " +
			fmt.Sprintf("%dx%d: %v", current.BaseWidth, current.BaseHeight, err)}
	}
	outW, outH := outputSize(baseW, baseH)
	want := obs.VideoSettings{
		FPSNumerator: outputFPS, FPSDenominator: 1,
		BaseWidth: baseW, BaseHeight: baseH,
		OutputWidth: outW, OutputHeight: outH,
	}
	if err := c.SetVideoSettings(ctx, want); err != nil {
		return []string{fmt.Sprintf("video %dx%d at %d fps: obs would not set it: %v", outW, outH, outputFPS, err)}
	}
	got, err := c.VideoSettings(ctx)
	if err != nil || got != want {
		return []string{fmt.Sprintf("video: wanted %+v, obs has %+v (error %v)", want, got, err)}
	}
	r.canvasW, r.canvasH = baseW, baseH
	return nil
}

// primaryDisplay is the pixel size of the primary display, the one whose corner is at 0,0.
func (r *Recorder) primaryDisplay(ctx context.Context) (w, h int, err error) {
	if r.cfg.DisplaySize != nil {
		if w, h := r.cfg.DisplaySize(); w > 0 && h > 0 {
			return w, h, nil // true pixels; OBS reports the display scaled by Windows' zoom
		}
	}
	monitors, err := r.currentClient().Monitors(ctx)
	if err != nil {
		return 0, 0, err
	}
	for _, m := range monitors {
		if m.X == 0 && m.Y == 0 && m.Width > 0 && m.Height > 0 {
			return m.Width, m.Height, nil
		}
	}
	if len(monitors) > 0 && monitors[0].Width > 0 && monitors[0].Height > 0 {
		return monitors[0].Width, monitors[0].Height, nil
	}
	return 0, 0, fmt.Errorf("obs listed no usable display (%d found)", len(monitors))
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

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
