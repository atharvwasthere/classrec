package manifest

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Status string

const (
	Pending Status = "pending"
	Running Status = "running"
	Done    Status = "done"
	Failed  Status = "failed"
)

type Stage struct {
	Status     Status     `json:"status"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Error      string     `json:"error,omitempty"`
}

type Source struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	MtimeNS int64  `json:"mtime_ns"`
}

// APITiming is what a real (billed) Sarvam call cost in wall time for one chunk.
type APITiming struct {
	JobID      string `json:"job_id"`
	UploadMS   int64  `json:"upload_ms"`
	JobWallMS  int64  `json:"job_wall_ms"`
	DownloadMS int64  `json:"download_ms"`
}

type Chunk struct {
	Index       int        `json:"index"`
	File        string     `json:"file"`
	StartSec    float64    `json:"start_sec"`
	DurationSec float64    `json:"duration_sec"`
	SHA256      string     `json:"sha256"`
	API         *APITiming `json:"api,omitempty"`
}

// Job is the in-flight Sarvam batch job, persisted so a crash while polling
// resumes the same (already paid) job instead of creating a second one.
type Job struct {
	ID      string   `json:"id"`
	Started bool     `json:"started"`
	Files   []string `json:"files"`
	Params  string   `json:"params"` // fingerprint of the job parameters it was created with
}

type Manifest struct {
	Source      Source            `json:"source"`
	AudioSHA256 string            `json:"audio_sha256,omitempty"`
	DurationSec float64           `json:"duration_sec,omitempty"`
	Chunks      []Chunk           `json:"chunks,omitempty"`
	Job         *Job              `json:"job,omitempty"`
	Stages      map[string]*Stage `json:"stages"`

	path string
	now  func() time.Time
}

func New(path string) *Manifest {
	return &Manifest{Stages: map[string]*Stage{}, path: path, now: time.Now}
}

// Load returns an empty manifest when the file does not exist yet.
func Load(path string) (*Manifest, error) {
	m := New(path)
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if m.Stages == nil {
		m.Stages = map[string]*Stage{}
	}
	return m, nil
}

func (m *Manifest) Save() error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.path + ".tmp"
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, m.path)
}

func (m *Manifest) IsDone(name string) bool {
	s := m.Stages[name]
	return s != nil && s.Status == Done
}

func (m *Manifest) Begin(name string) error {
	t := m.now()
	m.Stages[name] = &Stage{Status: Running, StartedAt: &t}
	return m.Save()
}

func (m *Manifest) Finish(name string, runErr error) error {
	s := m.Stages[name]
	if s == nil {
		s = &Stage{}
		m.Stages[name] = s
	}
	t := m.now()
	s.FinishedAt = &t
	if runErr != nil {
		s.Status, s.Error = Failed, runErr.Error()
	} else {
		s.Status, s.Error = Done, ""
	}
	return m.Save()
}

// SourceChanged reports whether the video differs from what the manifest was built for.
func (m *Manifest) SourceChanged(src Source) bool {
	return m.Source.Path != "" && m.Source != src
}

// Reset forgets all progress and adopts a new source.
func (m *Manifest) Reset(src Source) {
	m.Source = src
	m.AudioSHA256, m.DurationSec = "", 0
	m.Chunks, m.Job = nil, nil
	m.Stages = map[string]*Stage{}
}
