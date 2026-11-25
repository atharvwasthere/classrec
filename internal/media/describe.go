package media

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Info is what ffprobe says about a recording.
type Info struct {
	Duration float64 // seconds
	SizeByte int64
	BitRate  int64 // bits per second, over the whole file
	Video    *VideoInfo
	Audio    []AudioInfo
}

// VideoInfo is the first video stream.
type VideoInfo struct {
	Codec         string
	Profile       string
	Width, Height int
	FPS           float64
	BitRate       int64 // 0 when the container does not say; Matroska usually does not
}

// AudioInfo is one audio stream.
type AudioInfo struct {
	Codec      string
	SampleRate int
	Channels   int
}

// Describe probes the streams and container of path.
func Describe(ctx context.Context, path string) (Info, error) {
	out, _, err := run(ctx, "ffprobe", "-v", "error", "-show_streams", "-show_format", "-of", "json", path)
	if err != nil {
		return Info{}, err
	}
	return parseDescribe([]byte(out))
}

func parseDescribe(raw []byte) (Info, error) {
	var probe struct {
		Streams []struct {
			Type       string `json:"codec_type"`
			Codec      string `json:"codec_name"`
			Profile    string `json:"profile"`
			Width      int    `json:"width"`
			Height     int    `json:"height"`
			AvgFrame   string `json:"avg_frame_rate"`
			BitRate    string `json:"bit_rate"`
			SampleRate string `json:"sample_rate"`
			Channels   int    `json:"channels"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
			Size     string `json:"size"`
			BitRate  string `json:"bit_rate"`
		} `json:"format"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return Info{}, fmt.Errorf("reading ffprobe output: %w", err)
	}
	var info Info
	info.Duration, _ = strconv.ParseFloat(probe.Format.Duration, 64)
	info.SizeByte, _ = strconv.ParseInt(probe.Format.Size, 10, 64)
	info.BitRate, _ = strconv.ParseInt(probe.Format.BitRate, 10, 64)
	if info.BitRate == 0 && info.Duration > 0 {
		info.BitRate = int64(float64(info.SizeByte) * 8 / info.Duration)
	}
	for _, s := range probe.Streams {
		switch s.Type {
		case "video":
			if info.Video != nil {
				continue
			}
			v := &VideoInfo{Codec: s.Codec, Profile: s.Profile, Width: s.Width, Height: s.Height, FPS: parseRate(s.AvgFrame)}
			v.BitRate, _ = strconv.ParseInt(s.BitRate, 10, 64)
			info.Video = v
		case "audio":
			rate, _ := strconv.Atoi(s.SampleRate)
			info.Audio = append(info.Audio, AudioInfo{Codec: s.Codec, SampleRate: rate, Channels: s.Channels})
		}
	}
	return info, nil
}

// parseRate reads ffprobe's "15/1" style frame rates.
func parseRate(s string) float64 {
	num, den, found := strings.Cut(s, "/")
	n, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0
	}
	if !found {
		return n
	}
	d, err := strconv.ParseFloat(den, 64)
	if err != nil || d == 0 {
		return 0
	}
	return n / d
}

// Frame writes the picture at second at of the video as a PNG.
func Frame(ctx context.Context, in, out string, at float64) error {
	_, _, err := run(ctx, "ffmpeg", "-v", "error", "-y", "-ss", strconv.FormatFloat(at, 'f', 2, 64),
		"-i", in, "-frames:v", "1", out)
	return err
}
