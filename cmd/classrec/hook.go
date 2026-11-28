package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"classrec/internal/media"
	"classrec/internal/pipeline"
	"classrec/internal/recorder"
	"classrec/internal/sarvam"
)

// afterClass transcribes a filed class in mode full, in the background so the agent keeps
// watching for the next class. Mode record, the default, never calls Sarvam.
func afterClass(ctx context.Context, mode Mode, class recorder.Class, logf func(string, ...any),
	process func(ctx context.Context, dir string, logf func(string, ...any)) error) {
	switch {
	case class.Dir == "":
		return
	case mode != modeFull:
		logf("mode %s: no transcript; run `classrec process %q` to make one", mode, class.Dir)
		return
	case class.Pending > 0:
		logf("not transcribing %s yet: %d piece(s) still in staging", class.Dir, class.Pending)
		return
	}
	go func() {
		if err := process(context.WithoutCancel(ctx), class.Dir, logf); err != nil {
			logf("transcribing %s failed: %v (rerun `classrec process %q` to resume)", class.Dir, err, class.Dir)
			return
		}
		logf("transcript ready in %s", class.Dir)
	}()
}

// processClass turns a class folder into a transcript: it joins the pieces in order and runs the
// pipeline into dir\transcript.
// It is resumable: a rerun reuses the joined video and every finished stage.
func processClass(ctx context.Context, dir string, logf func(string, ...any)) error {
	return processClassUntil(ctx, dir, "", logf)
}

// processClassUntil is processClass that ends after the named pipeline stage, "" for all.
func processClassUntil(ctx context.Context, dir, stopAfter string, logf func(string, ...any)) error {
	info, err := recorder.ReadPieces(dir)
	if err != nil {
		return err
	}
	if len(info.Pieces) == 0 {
		return errors.New("the class has no pieces")
	}
	video, err := joinPieces(ctx, dir, info)
	if err != nil {
		return err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	p, err := pipeline.New(pipeline.Config{
		Video: video, Out: filepath.Join(dir, "transcript"), Date: info.Began.Format("2006-01-02"), StopAfter: stopAfter,
		CacheDir: filepath.Join(cache, "classrec", "raw"),
		Params:   sarvam.JobParams{Model: "saaras:v4", Mode: "codemix", LanguageCode: "hi-IN", WithTimestamps: true},
	}, &lazyBatch{envFile: defaultEnvFile()}, logf)
	if err != nil {
		return err
	}
	return p.Run(ctx)
}

// joinPieces returns the class as one video, joining the pieces once into "<folder> - all
// parts.mkv" (joined.mkv in folders from before v0.1.12).
func joinPieces(ctx context.Context, dir string, info recorder.Pieces) (string, error) {
	parts := make([]string, len(info.Pieces))
	for i, p := range info.Pieces {
		parts[i] = filepath.Join(dir, p.File)
	}
	if len(parts) == 1 {
		return parts[0], nil
	}
	if old := filepath.Join(dir, "joined.mkv"); exists(old) {
		return old, nil
	}
	joined := filepath.Join(dir, filepath.Base(dir)+" - all parts.mkv")
	if exists(joined) {
		return joined, nil
	}
	return joined, media.Concat(ctx, parts, joined)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
