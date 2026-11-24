package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"classrec/internal/manifest"
	"classrec/internal/sarvam"
)

// maxFilesPerJob is Sarvam's documented limit for one batch job.
const maxFilesPerJob = 20

type Batcher interface {
	RunBatch(ctx context.Context, req sarvam.BatchRequest) (map[string]sarvam.FileResult, error)
}

func (p *Pipeline) cachePath(c manifest.Chunk) string {
	return filepath.Join(p.Cfg.CacheDir, c.SHA256+"."+p.Cfg.Params.Fingerprint()+".json")
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
	results, err := p.Batch.RunBatch(ctx, sarvam.BatchRequest{
		Params: p.Cfg.Params, Files: need, Resume: p.M.Job, Save: p.M.SetJob, Logf: p.Logf,
	})

	byName := map[string]manifest.Chunk{}
	for _, c := range p.M.Chunks {
		byName[filepath.Base(c.File)] = c
	}
	for name, r := range results {
		if werr := writeFileAtomic(p.cachePath(byName[name]), r.Raw); werr != nil {
			return werr
		}
		timing := manifest.APITiming{JobID: r.JobID, UploadMS: r.UploadMS, JobWallMS: r.JobWallMS, DownloadMS: r.DownloadMS}
		if rerr := p.M.RecordAPI(name, timing); rerr != nil {
			return rerr
		}
	}
	if serr := p.M.Save(); serr != nil {
		return serr
	}
	return err
}
