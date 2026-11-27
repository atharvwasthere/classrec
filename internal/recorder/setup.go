package recorder

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"classrec/internal/detect"
	"classrec/internal/obs"
)

// What `classrec install` writes into the classrec profile, and what the recorder checks for.
const (
	Encoder      = "obs_qsv11_v2" // QuickSync H.264 in OBS 33
	OutputWidth  = 1280
	OutputHeight = 720
	FPS          = 15
	BitrateKbps  = 1000
	KeyframeSecs = 2
)

const (
	codeNotReady         = 207 // obs-websocket: OBS is still starting up
	notReadyAttempts     = 30
	codeOutputRunning    = 500 // obs-websocket: StartRecord while already recording
	codeOutputNotRunning = 501 // StopRecord while not recording
	codeResourceExists   = 601 // CreateScene or CreateInput for a name in use
	profileSettle        = 2 * time.Second
	windowPriorityExe    = 2 // OBS window capture: if the title is gone, match the exe
	// windowCaptureWGC is OBS's "Windows 10 (1903 and up)" capture method. "Automatic" picks BitBlt
	// for Zoom, which records Zoom's toolbars but leaves the video and shared screen blank white
	// (seen 2026-11-27); WGC captures the window as it is on screen.
	windowCaptureWGC       = 2
	alignCenter            = 0 // OBS alignment bits: left 1, right 2, top 4, bottom 8; none is centre
	alignTopLeft           = 1 | 4
	boundsScaleToFitInside = "OBS_BOUNDS_SCALE_INNER"
)

// The consumer-side views of *obs.Client each setup step needs.
type (
	profileSetup interface {
		Profiles(ctx context.Context) ([]string, string, error)
		SetCurrentProfile(ctx context.Context, name string) error
		ProfileParameter(ctx context.Context, category, name string) (string, error)
		VideoSettings(ctx context.Context) (obs.VideoSettings, error)
	}
	folderSetup interface {
		SetRecordDirectory(ctx context.Context, dir string) error
		RecordDirectory(ctx context.Context) (string, error)
	}
	sceneSetup interface {
		CreateScene(ctx context.Context, name string) error
		CreateInput(ctx context.Context, scene, name, kind string, settings map[string]any) error
		SetInputSettings(ctx context.Context, name string, settings map[string]any) error
		SceneItemID(ctx context.Context, scene, source string) (int, error)
		SetSceneItemTransform(ctx context.Context, scene string, itemID int, transform map[string]any) error
		SetCurrentScene(ctx context.Context, name string) error
	}
	audioSetup interface {
		GlobalAudioInputs(ctx context.Context) ([]string, error)
		SetInputMute(ctx context.Context, name string, muted bool) error
	}
)

// setUp gets OBS ready to record the target and returns what it could not do or found different.
func (r *Recorder) setUp(ctx context.Context) []string {
	var problems []string
	note := func(what string, err error) {
		if err != nil {
			problems = append(problems, what+": "+err.Error())
		}
	}
	canvas, drift, err := useProfile(ctx, r.client, r.cfg.Sleep, r.cfg.ProfileDir)
	problems = append(problems, drift...)
	note("profile", err)
	note("record folder", setRecordFolder(ctx, r.client, r.staging()))
	audio, err := setUpScene(ctx, r.client, r.target, canvas)
	note("scene", err)
	if audio != "" {
		r.audioName = audio
	}
	note("muting desktop and mic audio", muteGlobalAudio(ctx, r.client))
	return problems
}

// useProfile selects the classrec profile and reports any setting that differs from what install
// wrote. It changes nothing else: settings are install's job, done while OBS is closed. It returns
// the canvas size the window must fit.
func useProfile(ctx context.Context, c profileSetup, sleep func(context.Context, time.Duration) error, profileDir string) (canvas [2]int, drift []string, err error) {
	names, current, err := c.Profiles(ctx)
	if err != nil {
		return canvas, nil, err
	}
	if current != ProfileName {
		if !contains(names, ProfileName) {
			return canvas, nil, errors.New("obs has no classrec profile: run classrec install")
		}
		if err := c.SetCurrentProfile(ctx, ProfileName); err != nil {
			return canvas, nil, err
		}
		if err := sleep(ctx, profileSettle); err != nil { // OBS answers before it has loaded the profile
			return canvas, nil, err
		}
	}
	for _, p := range [][3]string{{"Output", "Mode", "Advanced"}, {"AdvOut", "RecFormat2", "mkv"}, {"AdvOut", "RecEncoder", Encoder}} {
		if got, err := c.ProfileParameter(ctx, p[0], p[1]); err != nil || got != p[2] {
			drift = append(drift, fmt.Sprintf("%s/%s is %q, not %q", p[0], p[1], got, p[2]))
		}
	}
	video, err := c.VideoSettings(ctx)
	if err != nil {
		return canvas, drift, err
	}
	// the width follows the screen's shape (1280 only for 16:9), so only the height is fixed
	if video.OutputHeight != OutputHeight || video.FPSNumerator != FPS*video.FPSDenominator {
		drift = append(drift, fmt.Sprintf("video is %dx%d at %d/%d fps, not %d high at %d", video.OutputWidth, video.OutputHeight,
			video.FPSNumerator, video.FPSDenominator, OutputHeight, FPS))
	}
	if profileDir != "" {
		want := obs.EncoderSettings{RateControl: "CBR", Bitrate: BitrateKbps, KeyintSec: KeyframeSecs}
		if got, err := obs.ReadEncoderSettings(profileDir); err != nil || got != want {
			drift = append(drift, fmt.Sprintf("encoder is %+v, not %+v (%v)", got, want, err))
		}
	}
	return [2]int{video.BaseWidth, video.BaseHeight}, drift, nil
}

// setRecordFolder points OBS at staging and checks that it took.
func setRecordFolder(ctx context.Context, c folderSetup, staging string) error {
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return err
	}
	if err := c.SetRecordDirectory(ctx, staging); err != nil {
		return err
	}
	got, err := c.RecordDirectory(ctx)
	if err != nil {
		return err
	}
	clean := func(p string) string { return strings.ToLower(filepath.Clean(filepath.FromSlash(p))) }
	if clean(got) != clean(staging) {
		return fmt.Errorf("obs records into %q, not %q", got, staging)
	}
	return nil
}

// setUpScene makes sure the app's scene has a window capture and an app-audio capture pointed at
// the meeting window, fits the window to the canvas, and selects the scene. It returns the audio
// input's name, set even if a later step fails.
func setUpScene(ctx context.Context, c sceneSetup, t Target, canvas [2]int) (audioInput string, err error) {
	spec, ok := detect.SpecFor(t.App)
	if !ok {
		return "", fmt.Errorf("unknown app %q", t.App)
	}
	scene, video, audio := spec.SceneName, spec.SceneName+" video", spec.SceneName+" audio"
	if err := ignoreExists(c.CreateScene(ctx, scene)); err != nil {
		return "", err
	}
	window := windowSpec(spec, t)
	for _, in := range []struct {
		name, kind string
		settings   map[string]any
	}{
		{video, "window_capture", map[string]any{"window": window, "priority": windowPriorityExe, "method": windowCaptureWGC, "cursor": false}},
		{audio, "wasapi_process_output_capture", map[string]any{"window": window, "priority": windowPriorityExe}},
	} {
		err := ignoreExists(c.CreateInput(ctx, scene, in.name, in.kind, in.settings))
		if err == nil {
			err = c.SetInputSettings(ctx, in.name, in.settings) // an existing input still points at last class's window
		}
		if err != nil {
			return "", fmt.Errorf("%s: %w", in.name, err)
		}
	}
	if err := fitToCanvas(ctx, c, scene, video, canvas); err != nil {
		return audio, err
	}
	return audio, c.SetCurrentScene(ctx, scene)
}

// fitToCanvas scales the captured window to sit whole inside the canvas, shape kept, centred.
// Without it a window larger than the canvas is cropped.
func fitToCanvas(ctx context.Context, c sceneSetup, scene, source string, canvas [2]int) error {
	if canvas[0] <= 0 || canvas[1] <= 0 {
		return errors.New("canvas size unknown, so the window cannot be fitted to it")
	}
	id, err := c.SceneItemID(ctx, scene, source)
	if err != nil {
		return err
	}
	return c.SetSceneItemTransform(ctx, scene, id, map[string]any{
		"positionX": 0, "positionY": 0, "alignment": alignTopLeft,
		"boundsType": boundsScaleToFitInside, "boundsWidth": canvas[0], "boundsHeight": canvas[1],
		"boundsAlignment": alignCenter,
		"cropLeft":        0, "cropRight": 0, "cropTop": 0, "cropBottom": 0, "rotation": 0,
	})
}

// muteGlobalAudio silences Desktop Audio and Mic/Aux: they belong to every scene, so no scene can
// leave them out, and they would add room noise and echo.
func muteGlobalAudio(ctx context.Context, c audioSetup) error {
	names, err := c.GlobalAudioInputs(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, name := range names {
		errs = append(errs, c.SetInputMute(ctx, name, true))
	}
	return errors.Join(errs...)
}

// windowSpec is how OBS names a window: "title:class:exe", with ':' and '#' escaped.
func windowSpec(spec detect.AppSpec, t Target) string {
	escape := strings.NewReplacer("#", "#23", ":", "#3A").Replace
	class := t.Class
	if class == "" {
		class = spec.WindowClass
	}
	return escape(t.Title) + ":" + escape(class) + ":" + escape(spec.Exe)
}

func ignoreExists(err error) error {
	var refused *obs.RequestError
	if errors.As(err, &refused) && refused.Code == codeResourceExists {
		return nil
	}
	return err
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
