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
	if p.M.AudioSHA256 == "" {
		return false
	}
	sum, err := media.SHA256File(p.audioFile())
	return err == nil && sum == p.M.AudioSHA256
}

func (p *Pipeline) extract(ctx context.Context) error {
	videoDur, err := media.Probe(ctx, p.Cfg.Video)
	if err != nil {
		return err
	}
	adopted, err := p.adoptReusableAudio(ctx, videoDur)
	if err != nil {
		return err
	}
	if !adopted {
		p.Logf("extracting audio with ffmpeg (mono opus 32k)")
		if err := media.ExtractAudio(ctx, p.Cfg.Video, p.audioFile()); err != nil {
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
	p.M.AudioSHA256, p.M.DurationSec = sum, dur
	return nil
}

func (p *Pipeline) adoptReusableAudio(ctx context.Context, videoDur float64) (bool, error) {
	if p.Cfg.ReuseAudio == "" {
		return false, nil
	}
	dur, err := media.Probe(ctx, p.Cfg.ReuseAudio)
	if err != nil || math.Abs(dur-videoDur) > durationTolerance {
		p.Logf("not reusing %s (duration %.1fs vs video %.1fs), re-extracting", p.Cfg.ReuseAudio, dur, videoDur)
		return false, nil
	}
	p.Logf("reusing %s (duration matches the video)", p.Cfg.ReuseAudio)
	return true, copyFile(p.Cfg.ReuseAudio, p.audioFile())
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
