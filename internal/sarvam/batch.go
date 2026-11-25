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

// BatchFile is one audio file of a batch job.
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

// BatchRequest is one batch job to run.
type BatchRequest struct {
	Params JobParams
	Files  []BatchFile
	// Resume is the state saved by an earlier run. It is used only if it was made for the same files and parameters.
	Resume *BatchState
	// Save persists progress as the job advances; it gets nil once the job is over.
	Save func(*BatchState) error
	Logf func(string, ...any)
}

// FileResult is the raw output for one file and how long each step took.
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
	st := BatchState{Files: names, Params: req.Params.Fingerprint()}
	if r := req.Resume; r != nil && r.JobID != "" && r.Params == st.Params && slices.Equal(r.Files, names) {
		st = *r
	}
	uploadMS, err := c.startJob(ctx, req, &st)
	if err != nil {
		return nil, err
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
	var partial *filesFailedError
	if err == nil || errors.As(err, &partial) {
		if serr := req.Save(nil); serr != nil {
			return results, serr
		}
	}
	return results, err
}

// startJob creates the job unless st resumes one, then uploads the files and starts it unless
// that was done already, saving st after each step so a crash resumes the same paid job.
func (c *Client) startJob(ctx context.Context, req BatchRequest, st *BatchState) (uploadMS map[string]int64, err error) {
	persist := func() error {
		snapshot := *st
		return req.Save(&snapshot)
	}
	if st.JobID == "" {
		if st.JobID, err = c.initJob(ctx, req.Params); err != nil {
			return nil, fmt.Errorf("init job: %w", err)
		}
		if err := persist(); err != nil {
			return nil, err
		}
		req.Logf("created job %s", st.JobID)
	} else {
		req.Logf("resuming job %s", st.JobID)
	}
	if st.Started {
		return nil, nil
	}
	if uploadMS, err = c.upload(ctx, st.JobID, req); err != nil {
		return nil, err
	}
	if err := c.start(ctx, st.JobID); err != nil {
		// a lost answer may hide a start that worked; only a job still waiting was not started
		if s, sErr := c.status(ctx, st.JobID); sErr != nil || s.JobState == "Accepted" {
			return nil, fmt.Errorf("start job: %w", err)
		}
	}
	st.Started = true
	if err := persist(); err != nil {
		return nil, err
	}
	req.Logf("job started")
	return uploadMS, nil
}

// upload puts every file of req into the job and says how long each took.
func (c *Client) upload(ctx context.Context, jobID string, req BatchRequest) (map[string]int64, error) {
	names := make([]string, len(req.Files))
	for i, f := range req.Files {
		names[i] = f.Name
	}
	links, err := c.uploadLinks(ctx, jobID, names)
	if err != nil {
		return nil, fmt.Errorf("upload links: %w", err)
	}
	uploadMS := map[string]int64{}
	for _, f := range req.Files {
		t := time.Now()
		req.Logf("uploading %s", f.Name)
		if err := c.putFile(ctx, links[f.Name], f.Path); err != nil {
			return nil, fmt.Errorf("upload %s: %w", f.Name, err)
		}
		uploadMS[f.Name] = time.Since(t).Milliseconds()
	}
	return uploadMS, nil
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
		if err := c.opts.Retry.Sleep(ctx, c.opts.PollEvery); err != nil {
			return nil, fmt.Errorf("waiting for job %s (it keeps running; re-run to resume): %w", jobID, err)
		}
	}
}

func (c *Client) collect(ctx context.Context, jobID string, status *jobStatus, names []string,
	uploadMS map[string]int64, wall int64) (map[string]FileResult, error) {
	outputFor, failed := outputsByInput(status, names)
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
			if err == nil && !json.Valid(raw) {
				err = errors.New("output is not JSON")
			}
			if err != nil {
				return results, fmt.Errorf("download %s: %w", in, err)
			}
			results[in] = FileResult{Raw: raw, JobID: jobID, UploadMS: uploadMS[in], JobWallMS: wall, DownloadMS: time.Since(t).Milliseconds()}
		}
	}
	if len(failed) > 0 {
		return results, &filesFailedError{Files: failed}
	}
	return results, nil
}

// outputsByInput maps each input file that succeeded to its output file, and describes the rest.
func outputsByInput(status *jobStatus, names []string) (outputFor map[string]string, failed []string) {
	outputFor = map[string]string{}
	reported := map[string]bool{}
	for _, d := range status.JobDetails {
		if len(d.Inputs) == 0 {
			continue
		}
		in := d.Inputs[0].FileName
		reported[in] = true
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
		if !reported[n] {
			failed = append(failed, n+" (no result reported)")
		}
	}
	return outputFor, failed
}

// filesFailedError means the job finished but some files had no result. The job is
// over, so the caller should retry only those files in a new one.
type filesFailedError struct{ Files []string }

func (e *filesFailedError) Error() string { return "files failed: " + strings.Join(e.Files, "; ") }
