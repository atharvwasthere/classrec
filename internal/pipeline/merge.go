package pipeline

import (
	"context"
	"encoding/json"
	"os"

	"classrec/internal/transcript"
)

func (p *Pipeline) segmentsFile() string { return p.path("segments.json") }

func (p *Pipeline) mergeVerify() bool { return validJSONFile(p.segmentsFile()) }

func (p *Pipeline) merge(context.Context) error {
	raws := make([][]byte, len(p.m.Chunks))
	offsets := make([]float64, len(p.m.Chunks))
	for i, c := range p.m.Chunks {
		var err error
		if raws[i], err = os.ReadFile(p.rawPath(c)); err != nil {
			return err
		}
		offsets[i] = c.StartSec
	}
	segs, err := transcript.MergeChunks(raws, offsets)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(segs, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(p.segmentsFile(), b)
}

func (p *Pipeline) loadSegments() ([]transcript.Segment, error) {
	b, err := os.ReadFile(p.segmentsFile())
	if err != nil {
		return nil, err
	}
	var segs []transcript.Segment
	return segs, json.Unmarshal(b, &segs)
}
