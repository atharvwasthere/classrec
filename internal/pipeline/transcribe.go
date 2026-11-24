package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"classrec/internal/manifest"
	"classrec/internal/sarvam"
)

// maxFilesPerJob is Sarvam's documented limit for one batch job.
const maxFilesPerJob = 20

type Batcher interface {
	RunBatch(ctx context.Context, p sarvam.JobParams, files []sarvam.BatchFile, st sarvam.BatchState,
		save func(sarvam.BatchState) error, logf func(string, ...any)) (map[string]sarvam.FileResult, error)
}

func (p *Pipeline) paramsKey() string {
	b, _ := json.Marshal(p.Cfg.Params)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:4])
}

func (p *Pipeline) cachePath(c manifest.Chunk) string {
	return filepath.Join(p.Cfg.CacheDir, c.SHA256+"."+p.paramsKey()+".json")
}

func (p *Pipeline) rawPath(c manifest.Chunk) string {
	return p.path("raw", fmt.Sprintf("chunk_%03d.json", c.Index))
}

func validJSONFile(path string) bool {
	b, err := os.ReadFile(path)
	return err == nil && json.Valid(b)
}

func (p *Pipeline) transcribeVerify() bool {
	for _, c := range p.M.Chunks {
		if !validJSONFile(p.rawPath(c)) {
			return false
		}
	}
	return len(p.M.Chunks) > 0
}

func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".part"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (p *Pipeline) transcribe(ctx context.Context) error {
	var need []sarvam.BatchFile
	for _, c := range p.M.Chunks {
		if !validJSONFile(p.cachePath(c)) {
			need = append(need, sarvam.BatchFile{Name: filepath.Base(c.File), Path: p.chunkPath(c)})
		}
	}
	if len(need) == 0 {
		p.Logf("all %d chunks already transcribed (cache hit), no API call", len(p.M.Chunks))
	} else if err := p.runBatch(ctx, need); err != nil {
		return err
	}

	for _, c := range p.M.Chunks {
		raw, err := os.ReadFile(p.cachePath(c))
		if err != nil {
			return err
		}
		if err := writeFileAtomic(p.rawPath(c), raw); err != nil {
			return err
		}
	}
	return nil
}

func (p *Pipeline) runBatch(ctx context.Context, need []sarvam.BatchFile) error {
	if p.Batch == nil {
		return errors.New("no Sarvam client configured")
	}
	if len(need) > maxFilesPerJob {
		return fmt.Errorf("%d chunks exceed Sarvam's %d files per job", len(need), maxFilesPerJob)
	}
	names := make([]string, len(need))
	for i, f := range need {
		names[i] = f.Name
	}
	key := p.paramsKey()

	var st sarvam.BatchState
	if j := p.M.Job; j != nil && j.Params == key && slices.Equal(j.Files, names) {
		st = sarvam.BatchState{JobID: j.ID, Started: j.Started}
	}
	save := func(s sarvam.BatchState) error {
		p.M.Job = nil
		if s.JobID != "" {
			p.M.Job = &manifest.Job{ID: s.JobID, Started: s.Started, Files: names, Params: key}
		}
		return p.M.Save()
	}

	results, err := p.Batch.RunBatch(ctx, p.Cfg.Params, need, st, save, p.Logf)

	byName := map[string]*manifest.Chunk{}
	for i := range p.M.Chunks {
		byName[filepath.Base(p.M.Chunks[i].File)] = &p.M.Chunks[i]
	}
	for name, r := range results {
		c := byName[name]
		if werr := writeFileAtomic(p.cachePath(*c), r.Raw); werr != nil {
			return werr
		}
		c.API = &manifest.APITiming{JobID: r.JobID, UploadMS: r.UploadMS, JobWallMS: r.JobWallMS, DownloadMS: r.DownloadMS}
	}

	var partial *sarvam.FilesFailedError
	if err == nil || errors.As(err, &partial) {
		p.M.Job = nil // the job is over; anything still missing goes in a new one
	}
	if serr := p.M.Save(); serr != nil {
		return serr
	}
	return err
}
