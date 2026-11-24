package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"classrec/internal/transcript"
)

func (p *Pipeline) segmentsFile() string { return p.path("segments.json") }

func (p *Pipeline) mergeVerify() bool { return validJSONFile(p.segmentsFile()) }

func (p *Pipeline) merge(context.Context) error {
	parts := make([][]transcript.Segment, len(p.M.Chunks))
	offsets := make([]float64, len(p.M.Chunks))
	for i, c := range p.M.Chunks {
		raw, err := os.ReadFile(p.rawPath(c))
		if err != nil {
			return err
		}
		if parts[i], err = transcript.ParseChunk(raw); err != nil {
			return fmt.Errorf("chunk %d: %w", c.Index, err)
		}
		offsets[i] = c.StartSec
	}
	b, err := json.MarshalIndent(transcript.Merge(parts, offsets), "", "  ")
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
