package sarvam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type BatchFile struct {
	Name string // unique within the job, e.g. chunk_000.ogg
	Path string
}

// BatchState is what the caller persists so a crash resumes the same paid job.
type BatchState struct {
	JobID   string
	Started bool
}

type FileResult struct {
	Raw        []byte
	JobID      string
	UploadMS   int64
	JobWallMS  int64 // observed from this process starting/resuming the job until it completed
	DownloadMS int64
}

// RunBatch uploads every file in one job, starts it, polls to completion and downloads
// each output. Files that succeeded are returned even when others failed (err is then non-nil).
func (c *Client) RunBatch(ctx context.Context, p JobParams, files []BatchFile, st BatchState,
	save func(BatchState) error, logf func(string, ...any)) (map[string]FileResult, error) {

	names := make([]string, len(files))
	for i, f := range files {
		names[i] = f.Name
	}
	uploadMS := map[string]int64{}

	if st.JobID == "" {
		id, err := c.InitJob(ctx, p)
		if err != nil {
			return nil, fmt.Errorf("init job: %w", err)
		}
		st = BatchState{JobID: id}
		if err := save(st); err != nil {
			return nil, err
		}
		logf("created job %s", id)
	} else {
		logf("resuming job %s", st.JobID)
	}

	if !st.Started {
		links, err := c.UploadLinks(ctx, st.JobID, names)
		if err != nil {
			return nil, fmt.Errorf("upload links: %w", err)
		}
		for _, f := range files {
			t := time.Now()
			logf("uploading %s", f.Name)
			if err := c.PutFile(ctx, links[f.Name], f.Path); err != nil {
				return nil, fmt.Errorf("upload %s: %w", f.Name, err)
			}
			uploadMS[f.Name] = time.Since(t).Milliseconds()
		}
		if err := c.Start(ctx, st.JobID); err != nil {
			if s, sErr := c.Status(ctx, st.JobID); sErr != nil || s.JobState == "Accepted" {
				return nil, fmt.Errorf("start job: %w", err)
			}
		}
		st.Started = true
		if err := save(st); err != nil {
			return nil, err
		}
		logf("job started")
	}

	waitStart := time.Now()
	status, err := c.waitDone(ctx, st.JobID, logf)
	if err != nil {
		return nil, err
	}
	wall := time.Since(waitStart).Milliseconds()
	if status.JobState == "Failed" {
		_ = save(BatchState{}) // a failed job can't be resumed
		return nil, fmt.Errorf("job %s failed: %s", st.JobID, status.ErrorMessage)
	}

	return c.collect(ctx, st.JobID, status, names, uploadMS, wall)
}

func (c *Client) waitDone(ctx context.Context, jobID string, logf func(string, ...any)) (*JobStatus, error) {
	last := ""
	for {
		s, err := c.Status(ctx, jobID)
		if err != nil {
			return nil, fmt.Errorf("poll job %s (it keeps running; re-run to resume): %w", jobID, err)
		}
		if s.JobState != last {
			logf("job state: %s", s.JobState)
			last = s.JobState
		}
		if s.JobState == "Completed" || s.JobState == "Failed" {
			return s, nil
		}
		if err := c.Retry.Sleep(ctx, c.PollEvery); err != nil {
			return nil, fmt.Errorf("waiting for job %s (it keeps running; re-run to resume): %w", jobID, err)
		}
	}
}

func (c *Client) collect(ctx context.Context, jobID string, status *JobStatus, names []string,
	uploadMS map[string]int64, wall int64) (map[string]FileResult, error) {

	outputFor := map[string]string{} // input name -> output name
	var failed []string
	for _, d := range status.JobDetails {
		if len(d.Inputs) == 0 {
			continue
		}
		in := d.Inputs[0].FileName
		if d.State == "Success" && len(d.Outputs) > 0 {
			outputFor[in] = d.Outputs[0].FileName
			continue
		}
		msg := d.State
		if d.ErrorMessage != nil {
			msg += ": " + *d.ErrorMessage
		}
		failed = append(failed, in+" ("+msg+")")
	}
	for _, n := range names {
		if _, ok := outputFor[n]; !ok && !containsPrefix(failed, n) {
			failed = append(failed, n+" (no result reported)")
		}
	}

	results := map[string]FileResult{}
	if len(outputFor) > 0 {
		outs := make([]string, 0, len(outputFor))
		for _, o := range outputFor {
			outs = append(outs, o)
		}
		links, err := c.DownloadLinks(ctx, jobID, outs)
		if err != nil {
			return nil, fmt.Errorf("download links: %w", err)
		}
		for in, out := range outputFor {
			t := time.Now()
			raw, err := c.GetBytes(ctx, links[out])
			if err != nil {
				return results, fmt.Errorf("download %s: %w", in, err)
			}
			if !json.Valid(raw) {
				return results, fmt.Errorf("download %s: output is not JSON", in)
			}
			results[in] = FileResult{Raw: raw, JobID: jobID, UploadMS: uploadMS[in], JobWallMS: wall, DownloadMS: time.Since(t).Milliseconds()}
		}
	}
	if len(failed) > 0 {
		return results, errors.New("files failed: " + strings.Join(failed, "; "))
	}
	return results, nil
}

func containsPrefix(list []string, name string) bool {
	for _, s := range list {
		if strings.HasPrefix(s, name+" ") {
			return true
		}
	}
	return false
}
