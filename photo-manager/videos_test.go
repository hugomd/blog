package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image/jpeg"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func videoFixture(t *testing.T, args ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture")
	args = append([]string{"-nostdin", "-hide_banner", "-loglevel", "error", "-threads", "2", "-filter_threads", "1"}, args...)
	args = append(args, path)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	if body, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate video: %v: %s", err, body)
	}
	return path
}

type inspectedVideo struct {
	Streams []struct {
		Width, Height int
		FrameRate     string            `json:"avg_frame_rate"`
		Codec         string            `json:"codec_name"`
		PixelFormat   string            `json:"pix_fmt"`
		Aspect        string            `json:"sample_aspect_ratio"`
		Tags          map[string]string `json:"tags"`
	} `json:"streams"`
	Format struct {
		Duration string            `json:"duration"`
		Tags     map[string]string `json:"tags"`
	} `json:"format"`
}

func inspectVideo(t *testing.T, path string) inspectedVideo {
	t.Helper()
	var result inspectedVideo
	body, err := runVideoTool(t.Context(), 20*time.Second, "ffprobe", "-v", "error", "-show_streams", "-show_format", "-of", "json", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestProcessVideo(t *testing.T) {
	for _, test := range []struct {
		name, size, rate, container, codec, aspect string
		width, height                              int
		audio                                      bool
	}{
		{"small MP4 no upscale", "320x180", "12", "mp4", "libx264", "1", 320, 180, true},
		{"landscape capped", "2048x1152", "60", "mp4", "libx264", "1", 1920, 1080, false},
		{"portrait MOV", "1152x2048", "24", "mov", "libx264", "1", 1080, 1920, false},
		{"anamorphic", "320x240", "24", "mp4", "libx264", "2", 320, 120, false},
		{"narrow pixels", "320x240", "24", "mp4", "libx264", "1/2", 160, 240, false},
		{"HEVC MOV", "320x180", "24", "mov", "libx265", "1", 320, 180, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := []string{"-f", "lavfi", "-i", "testsrc2=size=" + test.size + ":rate=" + test.rate}
			if test.audio {
				args = append(args, "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100")
			}
			args = append(args, "-t", "0.3", "-vf", "setsar="+test.aspect, "-c:v", test.codec, "-threads", "2", "-preset", "ultrafast")
			if test.codec == "libx265" {
				args = append(args, "-x265-params", "pools=1:frame-threads=1:log-level=error")
			}
			args = append(args, "-c:a", "aac", "-metadata", "title=private-title", "-metadata", "location=+12.3456+078.1234/", "-metadata:s:v:0", "handler_name=private-handler", "-f", test.container)
			source := videoFixture(t, args...)
			original := inspectVideo(t, source)
			if original.Format.Tags["title"] != "private-title" || original.Format.Tags["location"] == "" || original.Streams[0].Tags["handler_name"] != "private-handler" {
				t.Fatal("fixture is missing source metadata")
			}
			result, err := processVideo(t.Context(), source, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if result.Width != test.width || result.Height != test.height || result.Duration <= 0 || result.Duration > 1 {
				t.Fatalf("unexpected result: %+v", result)
			}
			encoded := inspectVideo(t, result.Path)
			video := encoded.Streams[0]
			if video.Codec != "h264" || video.PixelFormat != "yuv420p" || video.Aspect != "1:1" || video.Width != result.Width || video.Height != result.Height || videoRatio(video.FrameRate) > 30 {
				t.Fatalf("unexpected encoded stream: %+v", video)
			}
			if result.Duration != videoNumber(encoded.Format.Duration) {
				t.Fatal("duration does not describe output")
			}
			if test.audio && (len(encoded.Streams) != 2 || encoded.Streams[1].Codec != "aac") {
				t.Fatalf("missing AAC audio: %+v", encoded.Streams)
			}
			if encoded.Format.Tags["title"] != "" || encoded.Format.Tags["location"] != "" || encoded.Format.Tags["location-eng"] != "" {
				t.Fatalf("source metadata retained: %+v", encoded.Format.Tags)
			}
			if video.Tags["handler_name"] == "private-handler" {
				t.Fatal("source stream metadata retained")
			}
			body, err := os.ReadFile(result.Path)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Index(body, []byte("moov")) < 0 || bytes.Index(body, []byte("moov")) > bytes.Index(body, []byte("mdat")) {
				t.Fatal("MP4 is not faststart")
			}
			if len(result.Variants) != 3 {
				t.Fatalf("got %d posters", len(result.Variants))
			}
			for i, variant := range result.Variants {
				if variant.Size != []int{640, 1280, 2560}[i] || bytes.Contains(variant.Body, []byte("Exif")) || bytes.Contains(variant.Body, []byte("private-title")) {
					t.Fatal("unexpected poster size or metadata")
				}
				poster, err := jpeg.DecodeConfig(bytes.NewReader(variant.Body))
				if err != nil {
					t.Fatal(err)
				}
				if poster.Width > variant.Size || poster.Height > variant.Size || math.Abs(float64(poster.Width)/float64(poster.Height)-float64(result.Width)/float64(result.Height)) > 0.01 {
					t.Fatalf("poster aspect or dimensions: %+v", poster)
				}
			}
		})
	}
}

func TestProcessVideoRotation(t *testing.T) {
	source := videoFixture(t, "-f", "lavfi", "-i", "color=red:size=320x180:rate=10", "-vf", "drawbox=x=0:y=0:w=160:h=180:color=blue:t=fill", "-t", "0.3", "-c:v", "libx264", "-threads", "2", "-f", "mp4")
	rotated := videoFixture(t, "-i", source, "-c", "copy", "-metadata:s:v:0", "rotate=90", "-f", "mov")
	result, err := processVideo(t.Context(), rotated, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if result.Width != 180 || result.Height != 320 {
		t.Fatalf("rotation not applied: %dx%d", result.Width, result.Height)
	}
	poster, err := jpeg.Decode(bytes.NewReader(result.Variants[0].Body))
	if err != nil {
		t.Fatal(err)
	}
	// Positive MOV rotation turns the left blue half into the bottom blue half.
	r, _, b, _ := poster.At(90, 40).RGBA()
	if r <= b {
		t.Fatal("poster top should be red after rotation")
	}
	r, _, b, _ = poster.At(90, 280).RGBA()
	if b <= r {
		t.Fatal("poster bottom should be blue after rotation")
	}
	probe, err := probeVideo(t.Context(), result.Path)
	if err != nil {
		t.Fatal(err)
	}
	for _, side := range probe.Streams[0].SideData {
		if side.Rotation != 0 {
			t.Fatal("encoded video retains rotation")
		}
	}
}

func TestProcessVideoRejectsUnsuitableInput(t *testing.T) {
	invalid := filepath.Join(t.TempDir(), "fake.mp4")
	if err := os.WriteFile(invalid, []byte("not a video"), 0600); err != nil {
		t.Fatal(err)
	}
	audio := videoFixture(t, "-f", "lavfi", "-i", "sine=frequency=440", "-t", "0.1", "-c:a", "aac", "-f", "mp4")
	other := videoFixture(t, "-f", "lavfi", "-i", "color=size=32x32:rate=1", "-t", "1", "-c:v", "libx264", "-threads", "2", "-f", "matroska")
	tooLong := videoFixture(t, "-f", "lavfi", "-i", "color=size=16x16:rate=1", "-t", "601", "-c:v", "libx264", "-threads", "2", "-preset", "ultrafast", "-f", "mp4")
	tooWide := videoFixture(t, "-f", "lavfi", "-i", "color=size=16002x2:rate=1", "-frames:v", "1", "-c:v", "png", "-threads", "2", "-f", "mov")
	threeGP := videoFixture(t, "-f", "lavfi", "-i", "color=size=32x32:rate=1", "-t", "1", "-c:v", "libx264", "-threads", "2", "-f", "3gp")
	for name, source := range map[string]string{"invalid": invalid, "audio only": audio, "other container": other, "3GP container": threeGP, "too long": tooLong, "too wide": tooWide, "directory": t.TempDir(), "missing": invalid + "missing"} {
		t.Run(name, func(t *testing.T) {
			destination := t.TempDir()
			if _, err := processVideo(t.Context(), source, destination); err == nil {
				t.Fatal("unsuitable video accepted")
			}
			entries, err := os.ReadDir(destination)
			if err != nil || len(entries) != 0 {
				t.Fatalf("failed conversion left files: %v, %v", entries, err)
			}
		})
	}
}

func TestProcessVideoCanceled(t *testing.T) {
	source := videoFixture(t, "-f", "lavfi", "-i", "color=size=32x32:rate=1", "-t", "1", "-c:v", "libx264", "-threads", "2", "-f", "mp4")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := processVideo(ctx, source, t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}
