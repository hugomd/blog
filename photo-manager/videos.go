package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type videoResult struct {
	Width, Height int
	Duration      float64
	Path          string
	Variants      []derivative
}

type videoProbe struct {
	Streams []struct {
		Index             int    `json:"index"`
		CodecType         string `json:"codec_type"`
		Width             int    `json:"width"`
		Height            int    `json:"height"`
		SampleAspectRatio string `json:"sample_aspect_ratio"`
		AverageFrameRate  string `json:"avg_frame_rate"`
		Duration          string `json:"duration"`
		Disposition       struct {
			AttachedPic int `json:"attached_pic"`
		} `json:"disposition"`
		SideData []struct {
			Rotation float64 `json:"rotation"`
		} `json:"side_data_list"`
	} `json:"streams"`
	Format struct {
		Name     string `json:"format_name"`
		Duration string `json:"duration"`
		Tags     struct {
			MajorBrand string `json:"major_brand"`
		} `json:"tags"`
	} `json:"format"`
}

// Keep both successful probe output and diagnostics bounded, including on malformed input.
type videoBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (b *videoBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.buffer.Len() {
		return 0, fmt.Errorf("video tool output limit exceeded")
	}
	return b.buffer.Write(p)
}

func runVideoTool(ctx context.Context, timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = 2 * time.Second
	stdout := &videoBuffer{limit: 256 * 1024}
	stderr := &videoBuffer{limit: 64 * 1024}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%s failed: %w", name, err)
	}
	return stdout.buffer.Bytes(), nil
}

func probeVideo(ctx context.Context, path string) (videoProbe, error) {
	var probe videoProbe
	body, err := runVideoTool(ctx, 20*time.Second, "ffprobe",
		"-v", "error", "-threads", "2", "-max_pixels", "40000000", "-protocol_whitelist", "file",
		"-probesize", "10000000", "-analyzeduration", "10000000",
		"-show_entries", "stream=index,codec_type,width,height,sample_aspect_ratio,avg_frame_rate,duration:stream_disposition=attached_pic:stream_side_data=rotation:format=format_name,duration:format_tags=major_brand",
		"-of", "json", path)
	if err != nil {
		return probe, err
	}
	err = json.Unmarshal(body, &probe)
	return probe, err
}

func videoNumber(value string) float64 {
	n, _ := strconv.ParseFloat(value, 64)
	return n
}

func videoRatio(value string) float64 {
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == ':' || r == '/' })
	if len(parts) != 2 {
		return 0
	}
	return videoNumber(parts[0]) / videoNumber(parts[1])
}

func finiteVideoNumber(n float64) bool { return n > 0 && !math.IsNaN(n) && !math.IsInf(n, 0) }

// processVideo accepts local MP4/MOV uploads only. The caller owns the returned
// MP4 (and must remove it after storage); all intermediate files are removed here.
// A writable destinationDir and OS temporary directory are required.
func processVideo(ctx context.Context, sourcePath string, destinationDir string) (result videoResult, err error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	sourcePath, err = filepath.Abs(sourcePath)
	if err != nil {
		return result, err
	}
	info, err := os.Stat(sourcePath)
	if err != nil || !info.Mode().IsRegular() {
		return result, badRequest("Use a local MP4 or MOV video file.")
	}
	probe, err := probeVideo(ctx, sourcePath)
	if err != nil {
		return result, fmt.Errorf("probe video: %w", err)
	}
	brand := strings.TrimSpace(probe.Format.Tags.MajorBrand)
	allowedBrand := brand == "qt" || brand == "isom" || brand == "mp41" || brand == "mp42" || brand == "avc1" || brand == "M4V" || brand == "MSNV" || (len(brand) == 4 && strings.HasPrefix(brand, "iso") && brand[3] >= '2' && brand[3] <= '9')
	if probe.Format.Name != "mov,mp4,m4a,3gp,3g2,mj2" || !allowedBrand {
		return result, badRequest("Use an MP4 or MOV video container.")
	}
	streamIndex := -1
	for i, stream := range probe.Streams {
		if stream.CodecType == "video" && stream.Disposition.AttachedPic == 0 {
			streamIndex = i
			break
		}
	}
	if streamIndex < 0 {
		return result, badRequest("The upload must contain a video track.")
	}
	stream := probe.Streams[streamIndex]
	duration := videoNumber(probe.Format.Duration)
	streamDuration := videoNumber(stream.Duration)
	if !finiteVideoNumber(duration) || duration > 600 || (stream.Duration != "" && (!finiteVideoNumber(streamDuration) || streamDuration > 600)) || stream.Width < 2 || stream.Height < 2 || stream.Width > 16000 || stream.Height > 16000 || int64(stream.Width)*int64(stream.Height) > 40_000_000 {
		return result, badRequest("Use video up to 10 minutes, 40 megapixels and 16000 pixels per dimension.")
	}
	sar := 1.0
	if stream.SampleAspectRatio != "" && stream.SampleAspectRatio != "N/A" {
		sar = videoRatio(stream.SampleAspectRatio)
	}
	fps := videoRatio(stream.AverageFrameRate)
	if !finiteVideoNumber(sar) || sar < 0.01 || sar > 100 || !finiteVideoNumber(fps) || fps > 1000 {
		return result, badRequest("The video has an unsupported aspect ratio or frame rate.")
	}
	w, h := float64(stream.Width)*sar, float64(stream.Height)
	for _, side := range stream.SideData {
		rotation := math.Mod(side.Rotation, 360)
		if math.IsNaN(rotation) || math.Abs(rotation/90-math.Round(rotation/90)) > 0.001 {
			return result, badRequest("The video has unsupported rotation.")
		}
		if math.Abs(math.Mod(rotation, 180)) > 0.001 {
			w, h = h, w
		}
	}
	maxW, maxH := 1920.0, 1080.0
	if h > w {
		maxW, maxH = maxH, maxW
	}
	// Normalize non-square pixels by shrinking, never enlarging either coded axis.
	scale := math.Min(math.Min(1, 1/sar), math.Min(maxW/w, maxH/h))
	width, height := int(math.Floor(w*scale/2))*2, int(math.Floor(h*scale/2))*2
	if width < 2 || height < 2 {
		return result, badRequest("The video dimensions cannot be encoded.")
	}
	output, err := os.CreateTemp(destinationDir, "video-*.mp4")
	if err != nil {
		return result, err
	}
	outputPath, err := filepath.Abs(output.Name())
	closeErr := output.Close()
	if err != nil || closeErr != nil {
		os.Remove(output.Name())
		return result, fmt.Errorf("create video output: %v %v", err, closeErr)
	}
	defer func() {
		if err != nil {
			os.Remove(outputPath)
		}
	}()
	filter := fmt.Sprintf("scale=%d:%d,setsar=1,fps=%.8f", width, height, math.Min(fps, 30))
	_, err = runVideoTool(ctx, 15*time.Minute, "ffmpeg",
		"-nostdin", "-hide_banner", "-loglevel", "error", "-xerror", "-y",
		"-threads", "2", "-max_pixels", "40000000", "-filter_threads", "1", "-protocol_whitelist", "file",
		"-probesize", "10000000", "-analyzeduration", "10000000", "-i", sourcePath,
		"-map", fmt.Sprintf("0:%d", stream.Index), "-map", "0:a:0?",
		"-map_metadata", "-1", "-map_metadata:s", "-1", "-map_chapters", "-1", "-vf", filter,
		"-c:v", "libx264", "-threads", "2", "-preset", "medium", "-crf", "23", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-ac", "2", "-ar", "48000", "-b:a", "128k",
		"-t", "600", "-movflags", "+faststart", "-f", "mp4", outputPath)
	if err != nil {
		return result, fmt.Errorf("convert video: %w", err)
	}
	encoded, err := probeVideo(ctx, outputPath)
	if err != nil {
		return result, fmt.Errorf("probe encoded video: %w", err)
	}
	if len(encoded.Streams) == 0 || encoded.Streams[0].Width != width || encoded.Streams[0].Height != height || !finiteVideoNumber(videoNumber(encoded.Format.Duration)) || videoNumber(encoded.Format.Duration) > 600 {
		return result, fmt.Errorf("encoded video has invalid dimensions or duration")
	}
	temp, err := os.MkdirTemp("", "video-poster-*")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(temp)
	poster := filepath.Join(temp, "poster.jpg")
	_, err = runVideoTool(ctx, 30*time.Second, "ffmpeg",
		"-nostdin", "-hide_banner", "-loglevel", "error", "-threads", "2", "-filter_threads", "1",
		"-protocol_whitelist", "file", "-i", outputPath, "-map", "0:v:0", "-frames:v", "1",
		"-map_metadata", "-1", "-threads", "2", poster)
	if err != nil {
		return result, fmt.Errorf("create video poster: %w", err)
	}
	body, err := os.ReadFile(poster)
	if err != nil {
		return result, err
	}
	_, _, _, _, variants, err := processImage(body)
	if err != nil {
		return result, err
	}
	return videoResult{Width: width, Height: height, Duration: videoNumber(encoded.Format.Duration), Path: outputPath, Variants: variants}, nil
}
