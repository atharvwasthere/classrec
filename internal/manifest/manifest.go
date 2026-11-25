package manifest

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"classrec/internal/fsx"
	"classrec/internal/sarvam"
)

// Status is how far a stage got.
type Status string

// The stage statuses.
const (
	Pending Status = "pending"
	Running Status = "running"
	Done    Status = "done"
	Failed  Status = "failed"
)

// Stage is one pipeline stage's progress.
type Stage struct {
	Status     Status     `json:"status"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Error      string     `json:"error,omitempty"`
}

// Source identifies the video a manifest belongs to, so a changed video is not resumed.
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

// Chunk is one cut of the audio and its hash, which keys the transcription cache.
type Chunk struct {
	Index       int        `json:"index"`
	File        string     `json:"file"`
	StartSec    float64    `json:"start_sec"`
	DurationSec float64    `json:"duration_sec"`
	SHA256      string     `json:"sha256"`
	API         *APITiming `json:"api,omitempty"`
}

// Manifest records a run's progress in the output folder, so a rerun resumes where it stopped.
type Manifest struct {
	Source      Source             `json:"source"`
	AudioSHA256 string             `json:"audio_sha256,omitempty"`
	DurationSec float64            `json:"duration_sec,omitempty"`
	Date        string             `json:"date,omitempty"`
	Chunks      []Chunk            `json:"chunks,omitempty"`
	Job         *sarvam.BatchState `json:"job,omitempty"`
	Stages      map[string]*Stage  `json:"stages"`

	path string
	now  func() time.Time
}

// New returns an empty manifest that saves to path.
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

// Save writes the manifest atomically.
func (m *Manifest) Save() error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return fsx.WriteFileAtomic(m.path, b)
}

// IsDone reports whether the named stage finished.
func (m *Manifest) IsDone(name string) bool {
	s := m.Stages[name]
	return s != nil && s.Status == Done
}

// Begin marks the stage running and saves.
func (m *Manifest) Begin(name string) error {
	t := m.now()
	m.Stages[name] = &Stage{Status: Running, StartedAt: &t}
	return m.Save()
}

// Finish marks the stage done, or failed with runErr, and saves.
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

// Adopt records the source video. If it differs from the one this manifest was built for,
// all progress is dropped and Adopt reports true.
func (m *Manifest) Adopt(src Source) (reset bool) {
	reset = m.Source.Path != "" && m.Source != src
	if reset {
		m.AudioSHA256, m.DurationSec, m.Date = "", 0, ""
		m.Chunks, m.Job = nil, nil
		m.Stages = map[string]*Stage{}
	}
	m.Source = src
	return reset
}

// SetJob records the in-flight batch job (nil when none) and saves.
func (m *Manifest) SetJob(j *sarvam.BatchState) error {
	m.Job = j
	return m.Save()
}

// RecordAPI notes what a billed Sarvam call for the named chunk file cost in wall time.
func (m *Manifest) RecordAPI(chunkFile string, t APITiming) error {
	for i := range m.Chunks {
		if filepath.Base(m.Chunks[i].File) == chunkFile {
			m.Chunks[i].API = &t
			return nil
		}
	}
	return fmt.Errorf("no chunk named %s in the manifest", chunkFile)
}
