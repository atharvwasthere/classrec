package pipeline

import (
	"context"
	"encoding/json"
	"math"

	"classrec/internal/fsx"
	"classrec/internal/manifest"
)

// List price from https://docs.sarvam.ai/api/getting-started/pricing: ₹0.49 per minute (pay-as-you-go), billed per second, excl. GST.
const (
	ratePaisePerMinute = 49
	gstPercent         = 18
)

type money struct {
	BeforeGST float64 `json:"inr_before_gst"`
	GST       float64 `json:"inr_gst"`
	Total     float64 `json:"inr_total"`
}

type chunkCost struct {
	Index         int                 `json:"index"`
	File          string              `json:"file"`
	BilledSeconds int64               `json:"billed_seconds"`
	API           *manifest.APITiming `json:"api"` // null when the answer came from the cache
}

type costReport struct {
	Model            string      `json:"model"`
	Mode             string      `json:"mode"`
	AudioMinutesSent float64     `json:"audio_minutes_sent"`
	Cost             money       `json:"cost"`
	RatePerMinuteINR float64     `json:"rate_per_minute_inr"`
	GSTPercent       int         `json:"gst_percent"`
	Chunks           []chunkCost `json:"chunks"`
	Note             string      `json:"note"`
}

// costOf prices the chunks that were actually sent to the API, in whole paise.
func costOf(chunks []manifest.Chunk) (billedSeconds int64, m money) {
	for _, c := range chunks {
		if c.API != nil {
			billedSeconds += int64(math.Ceil(c.DurationSec))
		}
	}
	base := int64(math.Round(float64(billedSeconds) * ratePaisePerMinute / 60))
	gst := (base*gstPercent + 50) / 100
	return billedSeconds, money{BeforeGST: float64(base) / 100, GST: float64(gst) / 100, Total: float64(base+gst) / 100}
}

func (p *Pipeline) costFile() string { return p.path("cost.json") }

func (p *Pipeline) costVerify() bool { return validJSONFile(p.costFile()) }

func (p *Pipeline) cost(context.Context) error {
	billed, money := costOf(p.m.Chunks)
	out := costReport{
		Model:            p.cfg.Params.Model,
		Mode:             p.cfg.Params.Mode,
		AudioMinutesSent: float64(billed) / 60,
		Cost:             money,
		RatePerMinuteINR: float64(ratePaisePerMinute) / 100,
		GSTPercent:       gstPercent,
		Note:             "estimate from list price; job_wall_ms is shared by all chunks of a job because Sarvam batches them together",
	}
	for _, c := range p.m.Chunks {
		cc := chunkCost{Index: c.Index, File: c.File, API: c.API}
		if c.API != nil {
			cc.BilledSeconds = int64(math.Ceil(c.DurationSec))
		}
		out.Chunks = append(out.Chunks, cc)
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	return fsx.WriteFileAtomic(p.costFile(), append(b, '\n'))
}
