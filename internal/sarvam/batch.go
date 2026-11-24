package sarvam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

type BatchFile struct {
	Name string // unique within the job, e.g. chunk_000.ogg
	Path string
}

// BatchState is the job progress the caller persists, so a crash resumes the same paid job.
type BatchState struct {
	JobID   string   `json:"id"`
	Started bool     `json:"started"`
	Files   []string `json:"files"`
	Params  string   `json:"params"`
}

type BatchRequest struct {
	Params JobParams
	Files  []BatchFile
	// Resume is the state saved by an earlier run. It is used only if it was made for the same files and parameters.
	Resume *BatchState
	// Save persists progress as the job advances; it gets nil once the job is over.
	Save func(*BatchState) error
	Logf func(string, ...any)
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
func (c *Client) RunBatch(ctx context.Context, req BatchRequest) (map[string]FileResult, error) {
	names := make([]string, len(req.Files))
	for i, f := range req.Files {
		names[i] = f.Name
	}
	fingerprint := req.Params.Fingerprint()
	st := BatchState{Files: names, Params: fingerprint}
	if r := req.Resume; r != nil && r.JobID != "" && r.Params == fingerprint && slices.Equal(r.Files, names) {
		st = *r
	}
	persist := func() error {
		snapshot := st
		return req.Save(&snapshot)
	}
	uploadMS := map[string]int64{}

	if st.JobID == "" {
		id, err := c.initJob(ctx, req.Params)
		if err != nil {
			return nil, fmt.Errorf("init job: %w", err)
		}
		st.JobID = id
		if err := persist(); err != nil {
			return nil, err
		}
		req.Logf("created job %s", id)
	} else {
		req.Logf("resuming job %s", st.JobID)
	}

	if !st.Started {
		links, err := c.uploadLinks(ctx, st.JobID, names)
		if err != nil {
			return nil, fmt.Errorf("upload links: %w", err)
		}
		for _, f := range req.Files {
			t := time.Now()
			req.Logf("uploading %s", f.Name)
			if err := c.putFile(ctx, links[f.Name], f.Path); err != nil {
				return nil, fmt.Errorf("upload %s: %w", f.Name, err)
			}
			uploadMS[f.Name] = time.Since(t).Milliseconds()
		}
		if err := c.start(ctx, st.JobID); err != nil {
			if s, sErr := c.status(ctx, st.JobID); sErr != nil || s.JobState == "Accepted" {
				return nil, fmt.Errorf("start job: %w", err)
			}
		}
		st.Started = true
		if err := persist(); err != nil {
			return nil, err
		}
		req.Logf("job started")
	}

	waitStart := time.Now()
	status, err := c.waitDone(ctx, st.JobID, req.Logf)
	if err != nil {
		return nil, err
	}
	wall := time.Since(waitStart).Milliseconds()
	if status.JobState == "Failed" {
		_ = req.Save(nil) // a failed job can't be resumed
		return nil, fmt.Errorf("job %s failed: %s", st.JobID, status.ErrorMessage)
	}

	results, err := c.collect(ctx, st.JobID, status, names, uploadMS, wall)
	// The job is over unless a download failed; a finished job must not be resumed.
	var partial *FilesFailedError
	if err == nil || errors.As(err, &partial) {
		if serr := req.Save(nil); serr != nil {
			return results, serr
		}
	}
	return results, err
}

func (c *Client) waitDone(ctx context.Context, jobID string, logf func(string, ...any)) (*jobStatus, error) {
	last := ""
	for {
		s, err := c.status(ctx, jobID)
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

func (c *Client) collect(ctx context.Context, jobID string, status *jobStatus, names []string,
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
		links, err := c.downloadLinks(ctx, jobID, outs)
		if err != nil {
			return nil, fmt.Errorf("download links: %w", err)
		}
		for in, out := range outputFor {
			t := time.Now()
			raw, err := c.getBytes(ctx, links[out])
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
		return results, &FilesFailedError{Files: failed}
	}
	return results, nil
}

// FilesFailedError means the job finished but some files had no result. The job is
// over, so the caller should retry only those files in a new one.
type FilesFailedError struct{ Files []string }

func (e *FilesFailedError) Error() string { return "files failed: " + strings.Join(e.Files, "; ") }

func containsPrefix(list []string, name string) bool {
	for _, s := range list {
		if strings.HasPrefix(s, name+" ") {
			return true
		}
	}
	return false
}
