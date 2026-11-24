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
	"classrec/internal/sarvam"
)

const usage = `usage: classrec process <video> [flags]

  --out <dir>          output directory (default ./out/<video name>)
  --marks <file>       JSON array of seconds; writes marks.md with mark-120s..mark+180s
  --reuse-audio <ogg>  adopt already-extracted audio if its duration matches the video
  --stop-after <stage> extract | chunk | transcribe | merge | write | cost
  --model --mode --lang  Sarvam job parameters (defaults: saaras:v4, codemix, hi-IN)
  --cache <dir>        raw response cache (default: user cache dir)
  --env <file>         file holding SARVAM_API_KEY (default .env)
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 || args[0] != "process" {
		fmt.Fprint(os.Stderr, usage)
		return errors.New("expected the process command")
	}
	fs := flag.NewFlagSet("process", flag.ContinueOnError)
	out := fs.String("out", "", "")
	marks := fs.String("marks", "", "")
	reuse := fs.String("reuse-audio", "", "")
	stopAfter := fs.String("stop-after", "", "")
	model := fs.String("model", "saaras:v4", "")
	mode := fs.String("mode", "codemix", "")
	lang := fs.String("lang", "hi-IN", "")
	cache := fs.String("cache", "", "")
	envFile := fs.String("env", ".env", "")

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

	if *out == "" {
		base := filepath.Base(video)
		*out = filepath.Join("out", strings.TrimSuffix(base, filepath.Ext(base)))
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
		Video: video, Out: *out, MarksPath: *marks, ReuseAudio: *reuse, StopAfter: *stopAfter, CacheDir: *cache,
		Params: sarvam.JobParams{Model: *model, Mode: *mode, LanguageCode: *lang, WithTimestamps: true},
	}, logf)
	if err != nil {
		return err
	}
	p.Batch = &lazyBatch{envFile: *envFile}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
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

func (l *lazyBatch) RunBatch(ctx context.Context, p sarvam.JobParams, files []sarvam.BatchFile, st sarvam.BatchState,
	save func(sarvam.BatchState) error, logf func(string, ...any)) (map[string]sarvam.FileResult, error) {
	if l.client == nil {
		key, err := loadAPIKey(l.envFile)
		if err != nil {
			return nil, err
		}
		l.client = sarvam.New(key)
	}
	return l.client.RunBatch(ctx, p, files, st, save, logf)
}
