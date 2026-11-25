package recorder

import (
	"context"
	"fmt"
	"strings"

	"classrec/internal/detect"
)

const (
	codeOutputNotRunning = 501 // obs-websocket: StopRecord while not recording

	windowPriorityExe = 2 // OBS window capture: if the title is gone, match the exe
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
	c := r.client
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
	if err := c.SetCurrentScene(ctx, scene); err != nil {
		return fmt.Errorf("selecting scene %q: %w", scene, err)
	}
	r.audioName = audio
	return nil
}
