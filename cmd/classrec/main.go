// Command classrec turns a recorded class into a timestamped, searchable transcript.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"classrec/internal/pipeline"
	"classrec/internal/recorder"
	"classrec/internal/sarvam"
)

const usage = `usage: classrec process <video or class folder> [flags]
       classrec calibrate <recording.mkv>...   (sets the silence threshold from real classes)
       classrec agent [--debug-detect] [--interval 10s] [--mode record|full] [--root <folder>] [--obs-url ws://localhost:4455]
       classrec install [--root <folder>]   (OBS closed: writes the OBS profile; from C:\classrec also starts the agent at logon)
       classrec uninstall                   (stops starting the agent at logon)
       classrec selftest                    (checks everything; writes logs\selftest-<time>.txt)
       classrec record-test [--app meet|zoom] [--seconds 30]   (records a clip with the real OBS)
       classrec overlay-demo [--state green|amber|red|offer]

  --out <dir>          output directory (default ./out/<video name>)
  --reuse-audio <ogg>  adopt already-extracted audio if its duration matches the video
  --stop-after <stage> extract | chunk | transcribe | merge | write | cost
  --model --mode --lang  Sarvam job parameters (defaults: saaras:v4, codemix, hi-IN)
  --cache <dir>        raw response cache (default: user cache dir)
  --date YYYY-MM-DD    recording date; only needed when the file name is not a Zoom name and the video has no creation date
  --env <file>         file holding SARVAM_API_KEY (default: the one next to classrec)
`

//go:generate go run ../../tools/winres classrec.manifest rsrc_windows_amd64.syso

func main() {
	err := run(os.Args[1:])
	if errors.Is(err, errUpdated) {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if len(args) > 0 {
		switch args[0] {
		case "process":
			return runProcess(ctx, args)
		case "agent":
			return runAgent(ctx, args[1:])
		case "calibrate":
			return runCalibrate(ctx, args[1:])
		case "uninstall":
			return runUninstall(ctx, args[1:])
		case "selftest":
			return runSelftest(ctx, args[1:])
		case "install":
			return runInstall(ctx, args[1:])
		case "record-test":
			return runRecordTest(ctx, args[1:])
		case "overlay-demo":
			return runOverlayDemo(ctx, args[1:])
		}
	}
	fmt.Fprint(os.Stderr, usage)
	return errors.New("expected a command")
}

func runProcess(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("process", flag.ContinueOnError)
	out := fs.String("out", "", "")
	reuse := fs.String("reuse-audio", "", "")
	stopAfter := fs.String("stop-after", "", "")
	model := fs.String("model", "saaras:v4", "")
	mode := fs.String("mode", "codemix", "")
	lang := fs.String("lang", "hi-IN", "")
	cache := fs.String("cache", "", "")
	date := fs.String("date", "", "")
	envFile := fs.String("env", "", "")

	// flags may come before or after the video path
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return errors.New("missing <video>")
	}
	video := rest[0]
	if err := fs.Parse(rest[1:]); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if recorder.IsClassFolder(video) {
		logf := func(format string, a ...any) { fmt.Fprintf(os.Stderr, format+"\n", a...) }
		return processClassUntil(ctx, video, *stopAfter, logf)
	}

	if *out == "" {
		base := filepath.Base(video)
		*out = filepath.Join("out", strings.TrimSuffix(base, filepath.Ext(base)))
	}
	if *envFile == "" {
		*envFile = defaultEnvFile()
	}
	if *cache == "" {
		dir, err := os.UserCacheDir()
		if err != nil {
			return err
		}
		*cache = filepath.Join(dir, "classrec", "raw")
	}

	logf := func(format string, a ...any) { fmt.Fprintf(os.Stderr, format+"\n", a...) }
	p, err := pipeline.New(pipeline.Config{
		Video: video, Out: *out, Date: *date, ReuseAudio: *reuse, StopAfter: *stopAfter, CacheDir: *cache,
		Params: sarvam.JobParams{Model: *model, Mode: *mode, LanguageCode: *lang, WithTimestamps: true},
	}, &lazyBatch{envFile: *envFile}, logf)
	if err != nil {
		return err
	}

	if err := p.Run(ctx); err != nil {
		return err
	}
	logf("done: %s", *out)
	return nil
}

// lazyBatch reads the API key only when a Sarvam call is actually needed,
// so dry runs and fully cached runs work without one.
type lazyBatch struct {
	envFile string
	client  *sarvam.Client
}

func (l *lazyBatch) RunBatch(ctx context.Context, req sarvam.BatchRequest) (map[string]sarvam.FileResult, error) {
	if l.client == nil {
		key, err := loadAPIKey(l.envFile)
		if err != nil {
			return nil, err
		}
		l.client = sarvam.New(key)
	}
	return l.client.RunBatch(ctx, req)
}
