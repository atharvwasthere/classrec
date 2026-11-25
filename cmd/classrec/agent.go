package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"classrec/internal/desktop"
	"classrec/internal/detect"
)

// debugLine is one --debug-detect record: the raw sample, which replays in tests, plus the live verdict for reading by eye.
type debugLine struct {
	detect.Sample
	Obs   detect.Observation `json:"obs"`
	Phase detect.Phase       `json:"phase"`
	Event *detect.Event      `json:"event,omitempty"`
}

func runAgent(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	debug := fs.Bool("debug-detect", false, "")
	interval := fs.Duration("interval", 10*time.Second, "")
	modeFlag := fs.String("mode", "", "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	mode, source, err := agentMode(*modeFlag, settingsPath())
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "mode: %s (%s)\n", mode, source)
	if !*debug {
		return errors.New("only agent --debug-detect is implemented so far")
	}

	dir := filepath.Join(exeDir(), "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, "detect-"+time.Now().Format("2006-01-02_150405")+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	fmt.Fprintf(os.Stderr, "logging every %s to %s; leave this window open, Ctrl+C to stop\n", *interval, path)

	m := detect.Machine{Cfg: detect.DefaultConfig}
	enc := json.NewEncoder(f)
	tick := time.NewTicker(*interval)
	defer tick.Stop()
	for {
		s := desktop.Snapshot(time.Now())
		line := debugLine{Sample: s, Obs: detect.Classify(s)}
		line.Event = m.Step(s.Time, line.Obs)
		line.Phase = m.Phase
		if err := enc.Encode(line); err != nil {
			return err
		}
		if line.Event != nil {
			fmt.Fprintf(os.Stderr, "%s %s %s %q\n", s.Time.Format("15:04:05"), line.Event.Kind, line.Event.App, line.Event.Title)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}
