package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"classrec/internal/marks"
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

// isClassFolder reports whether path is a class folder the agent filed.
func isClassFolder(path string) bool {
	_, err := os.Stat(filepath.Join(path, recorder.PiecesFile))
	return err == nil
}

// processClass turns a class folder into a transcript: it joins the pieces in order, places the
// "I didn't get this" marks on the joined timeline, and runs the pipeline into dir\transcript.
// It is resumable: a rerun reuses the joined video and every finished stage.
func processClass(ctx context.Context, dir string, logf func(string, ...any)) error {
	return processClassUntil(ctx, dir, "", logf)
}

// processClassUntil is processClass that ends after the named pipeline stage, "" for all.
func processClassUntil(ctx context.Context, dir, stopAfter string, logf func(string, ...any)) error {
	var info recorder.Pieces
	content, err := os.ReadFile(filepath.Join(dir, recorder.PiecesFile))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(content, &info); err != nil {
		return fmt.Errorf("%s: %w", recorder.PiecesFile, err)
	}
	if len(info.Pieces) == 0 {
		return errors.New("the class has no pieces")
	}
	video, durations, err := joinPieces(ctx, dir, info)
	if err != nil {
		return err
	}
	marksPath, err := placeMarks(dir, info, durations)
	if err != nil {
		return err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	p, err := pipeline.New(pipeline.Config{
		Video: video, Out: filepath.Join(dir, "transcript"), Date: info.Began.Format("2006-01-02"), MarksPath: marksPath, StopAfter: stopAfter,
		CacheDir: filepath.Join(cache, "classrec", "raw"),
		Params:   sarvam.JobParams{Model: "saaras:v4", Mode: "codemix", LanguageCode: "hi-IN", WithTimestamps: true},
	}, &lazyBatch{envFile: defaultEnvFile()}, logf)
	if err != nil {
		return err
	}
	return p.Run(ctx)
}

// joinPieces returns the class as one video, joining the pieces once into joined.mkv, and each
// piece's duration.
func joinPieces(ctx context.Context, dir string, info recorder.Pieces) (video string, durations []float64, err error) {
	parts := make([]string, len(info.Pieces))
	for i, p := range info.Pieces {
		parts[i] = filepath.Join(dir, p.File)
		d, err := media.Probe(ctx, parts[i])
		if err != nil {
			return "", nil, fmt.Errorf("%s: %w", p.File, err)
		}
		durations = append(durations, d)
	}
	if len(parts) == 1 {
		return parts[0], durations, nil
	}
	joined := filepath.Join(dir, "joined.mkv")
	if _, err := os.Stat(joined); err == nil {
		return joined, durations, nil
	}
	return joined, durations, media.Concat(ctx, parts, joined)
}

// placeMarks writes the class's wall-clock marks as seconds into the joined video, for the
// pipeline's marks.md; "" when she made none.
func placeMarks(dir string, info recorder.Pieces, durations []float64) (string, error) {
	times, err := marks.ReadTimes(filepath.Join(dir, "marks.json"))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	starts := make([]time.Time, len(info.Pieces))
	for i, p := range info.Pieces {
		starts[i] = p.Start
	}
	encoded, err := json.Marshal(marks.ToSeconds(times, starts, durations))
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "marks-seconds.json")
	return path, os.WriteFile(path, encoded, 0o644)
}
