package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"classrec/internal/fsx"
	"classrec/internal/manifest"
	"classrec/internal/sarvam"
)

// maxFilesPerJob is Sarvam's documented limit for one batch job.
const maxFilesPerJob = 20

type Batcher interface {
	RunBatch(ctx context.Context, req sarvam.BatchRequest) (map[string]sarvam.FileResult, error)
}

func (p *Pipeline) cachePath(c manifest.Chunk) string {
	return filepath.Join(p.cfg.CacheDir, c.SHA256+"."+p.cfg.Params.Fingerprint()+".json")
}

func (p *Pipeline) rawPath(c manifest.Chunk) string {
	return p.path("raw", fmt.Sprintf("chunk_%03d.json", c.Index))
}

func validJSONFile(path string) bool {
	b, err := os.ReadFile(path)
	return err == nil && json.Valid(b)
}

func (p *Pipeline) transcribeVerify() bool {
	for _, c := range p.m.Chunks {
		if !validJSONFile(p.rawPath(c)) {
			return false
		}
	}
	return len(p.m.Chunks) > 0
}

func (p *Pipeline) transcribe(ctx context.Context) error {
	var need []sarvam.BatchFile
	for _, c := range p.m.Chunks {
		if !validJSONFile(p.cachePath(c)) {
			need = append(need, sarvam.BatchFile{Name: filepath.Base(c.File), Path: p.chunkPath(c)})
		}
	}
	if len(need) == 0 {
		p.logf("all %d chunks already transcribed (cache hit), no API call", len(p.m.Chunks))
	} else if err := p.runBatch(ctx, need); err != nil {
		return err
	}

	for _, c := range p.m.Chunks {
		raw, err := os.ReadFile(p.cachePath(c))
		if err != nil {
			return err
		}
		if err := fsx.WriteFileAtomic(p.rawPath(c), raw); err != nil {
			return err
		}
	}
	return nil
}

func (p *Pipeline) runBatch(ctx context.Context, need []sarvam.BatchFile) error {
	if p.batch == nil {
		return errors.New("no Sarvam client configured")
	}
	if len(need) > maxFilesPerJob {
		return fmt.Errorf("%d chunks exceed Sarvam's %d files per job", len(need), maxFilesPerJob)
	}
	results, err := p.batch.RunBatch(ctx, sarvam.BatchRequest{
		Params: p.cfg.Params, Files: need, Resume: p.m.Job, Save: p.m.SetJob, Logf: p.logf,
	})

	byName := map[string]manifest.Chunk{}
	for _, c := range p.m.Chunks {
		byName[filepath.Base(c.File)] = c
	}
	for name, r := range results {
		if werr := fsx.WriteFileAtomic(p.cachePath(byName[name]), r.Raw); werr != nil {
			return werr
		}
		timing := manifest.APITiming{JobID: r.JobID, UploadMS: r.UploadMS, JobWallMS: r.JobWallMS, DownloadMS: r.DownloadMS}
		if rerr := p.m.RecordAPI(name, timing); rerr != nil {
			return rerr
		}
	}
	if serr := p.m.Save(); serr != nil {
		return serr
	}
	return err
}
