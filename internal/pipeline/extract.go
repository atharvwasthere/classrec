package pipeline

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"

	"classrec/internal/media"
)

// durationTolerance is how far a reused audio file may drift from the video's length.
const durationTolerance = 1.5

func (p *Pipeline) audioFile() string { return p.path("audio.ogg") }

func (p *Pipeline) extractVerify() bool {
	if p.m.AudioSHA256 == "" {
		return false
	}
	sum, err := media.SHA256File(p.audioFile())
	return err == nil && sum == p.m.AudioSHA256
}

func (p *Pipeline) extract(ctx context.Context) error {
	videoDur, err := media.Probe(ctx, p.cfg.Video)
	if err != nil {
		return err
	}
	adopted, err := p.adoptReusableAudio(ctx, videoDur)
	if err != nil {
		return err
	}
	if !adopted {
		p.logf("extracting audio with ffmpeg (mono opus 32k)")
		if err := media.ExtractAudio(ctx, p.cfg.Video, p.audioFile()); err != nil {
			return err
		}
	}
	dur, err := media.Probe(ctx, p.audioFile())
	if err != nil {
		return err
	}
	if math.Abs(dur-videoDur) > durationTolerance {
		return fmt.Errorf("audio is %.1fs but video is %.1fs", dur, videoDur)
	}
	sum, err := media.SHA256File(p.audioFile())
	if err != nil {
		return err
	}
	p.m.AudioSHA256, p.m.DurationSec = sum, dur
	return nil
}

func (p *Pipeline) adoptReusableAudio(ctx context.Context, videoDur float64) (bool, error) {
	if p.cfg.ReuseAudio == "" {
		return false, nil
	}
	dur, err := media.Probe(ctx, p.cfg.ReuseAudio)
	if err != nil || math.Abs(dur-videoDur) > durationTolerance {
		p.logf("not reusing %s (duration %.1fs vs video %.1fs), re-extracting", p.cfg.ReuseAudio, dur, videoDur)
		return false, nil
	}
	p.logf("reusing %s (duration matches the video)", p.cfg.ReuseAudio)
	return true, copyFile(p.cfg.ReuseAudio, p.audioFile())
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}
