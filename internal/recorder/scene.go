package recorder

import (
	"context"
	"fmt"
	"strings"
	"time"

	"classrec/internal/detect"
)

const (
	codeOutputRunning    = 500 // obs-websocket: StartRecord while already recording
	codeOutputNotRunning = 501 // obs-websocket: StopRecord while not recording

	startCheckAttempts = 10
	startCheckEvery    = 500 * time.Millisecond

	windowPriorityExe = 2 // OBS window capture: if the title is gone, match the exe

	// OBS alignment bits: left 1, right 2, top 4, bottom 8; none set means centre.
	alignCenter  = 0
	alignTopLeft = 1 | 4
)

func sceneName(app string) string      { return "classrec - " + app }
func videoInputName(app string) string { return sceneName(app) + " video" }
func audioInputName(app string) string { return sceneName(app) + " audio" }

// exeFor is the exe each class app runs as.
func exeFor(t Target) string {
	if t.Exe != "" {
		return t.Exe
	}
	if t.App == detect.Zoom {
		return "Zoom.exe"
	}
	return "msedge.exe"
}

// windowSpec is how OBS names a window: "title:class:exe", with ':' and '#' in the parts escaped.
func windowSpec(t Target) string {
	escape := strings.NewReplacer("#", "#23", ":", "#3A").Replace
	class := t.Class
	if class == "" {
		class = "Chrome_WidgetWin_1"
		if t.App == detect.Zoom {
			class = "ConfMultiTabContentWndClass"
		}
	}
	return escape(t.Title) + ":" + escape(class) + ":" + escape(exeFor(t))
}

// setUpScene makes sure the app's scene exists with its two inputs, points them at the meeting
// window, and selects the scene. Scenes and inputs are the agent's own; ones she made are left alone.
func (r *Recorder) setUpScene(ctx context.Context) error {
	c := r.currentClient()
	app := r.target.App
	scene, video, audio := sceneName(app), videoInputName(app), audioInputName(app)

	scenes, err := c.Scenes(ctx)
	if err != nil {
		return err
	}
	if !contains(scenes, scene) {
		if err := c.CreateScene(ctx, scene); err != nil {
			return fmt.Errorf("creating scene %q: %w", scene, err)
		}
	}
	inputs, err := c.Inputs(ctx)
	if err != nil {
		return err
	}
	window := map[string]any{"window": windowSpec(r.target), "priority": windowPriorityExe}
	videoSet := map[string]any{"window": windowSpec(r.target), "priority": windowPriorityExe, "cursor": false}
	for _, in := range []struct {
		name, kind string
		settings   map[string]any
	}{
		{video, "window_capture", videoSet},
		{audio, "wasapi_process_output_capture", window},
	} {
		if contains(inputs, in.name) {
			err = c.SetInputSettings(ctx, in.name, in.settings)
		} else {
			err = c.CreateInput(ctx, scene, in.name, in.kind, in.settings)
		}
		if err != nil {
			return fmt.Errorf("setting up %q: %w", in.name, err)
		}
	}
	// the audio input exists now, so its level is usable even if the rest of the setup fails
	r.mu.Lock()
	r.audioName = audio
	r.mu.Unlock()

	if err := r.fitWindowToCanvas(ctx, scene, video); err != nil {
		return err
	}
	if err := c.SetCurrentScene(ctx, scene); err != nil {
		return fmt.Errorf("selecting scene %q: %w", scene, err)
	}
	return nil
}

// fitWindowToCanvas scales the captured window to sit inside the whole canvas with its shape
// kept, centred. Without it a window larger than the canvas is cropped.
func (r *Recorder) fitWindowToCanvas(ctx context.Context, scene, video string) error {
	if r.canvasW <= 0 || r.canvasH <= 0 {
		return fmt.Errorf("the canvas size is unknown, so %q cannot be fitted to it", video)
	}
	c := r.currentClient()
	id, err := c.SceneItemID(ctx, scene, video)
	if err != nil {
		return fmt.Errorf("finding %q in %q: %w", video, scene, err)
	}
	transform := map[string]any{
		"positionX": 0, "positionY": 0,
		"alignment":   alignTopLeft, // the item's own anchor: the bounds box starts at the canvas corner
		"boundsType":  "OBS_BOUNDS_SCALE_INNER",
		"boundsWidth": r.canvasW, "boundsHeight": r.canvasH,
		"boundsAlignment": alignCenter, // the window sits in the middle of the bounds box
		"cropLeft":        0, "cropRight": 0, "cropTop": 0, "cropBottom": 0,
		"rotation": 0,
	}
	if err := c.SetSceneItemTransform(ctx, scene, id, transform); err != nil {
		return fmt.Errorf("fitting %q to the canvas: %w", video, err)
	}
	return nil
}
