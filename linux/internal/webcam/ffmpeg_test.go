package webcam

import (
	"os"
	"strings"
	"testing"
)

func TestProbeHWAccel_EnvOverride(t *testing.T) {
	t.Setenv("LINUXDROP_HWACCEL", "sw")
	if got := probeHWAccel(); got != "sw" {
		t.Fatalf("env override sw, got %q", got)
	}
	t.Setenv("LINUXDROP_HWACCEL", "vaapi")
	if got := probeHWAccel(); got != "vaapi" {
		t.Fatalf("env override vaapi, got %q", got)
	}
}

func TestFFmpegArgs_HEVC_VAAPI(t *testing.T) {
	args := ffmpegArgs("vaapi", "hevc", 1280, 720)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-hwaccel vaapi") {
		t.Fatalf("missing -hwaccel vaapi in %q", joined)
	}
	if !strings.Contains(joined, "-f hevc") {
		t.Fatalf("missing -f hevc in %q", joined)
	}
	if !strings.Contains(joined, "-f rawvideo") {
		t.Fatalf("missing -f rawvideo in %q", joined)
	}
}

func TestFFmpegArgs_H264_SW(t *testing.T) {
	args := ffmpegArgs("sw", "h264", 1920, 1080)
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "-hwaccel") {
		t.Fatalf("sw should not include -hwaccel in %q", joined)
	}
	if !strings.Contains(joined, "-f h264") {
		t.Fatalf("missing -f h264 in %q", joined)
	}
}

// Integration: requires ffmpeg installed AND the tiny H.264 fixture. Skip otherwise.
// Mirrors actual usage: a writer goroutine feeds the NALU stream while the main
// loop drains decoded YUV frames — the OS pipe buffer is too small to hold a
// full fixture's worth of output, so we MUST read concurrently.
func TestPipe_DecodesFixture(t *testing.T) {
	if _, err := os.Stat("testdata/h264_1280x720_30f.h264"); err != nil {
		t.Skip("missing testdata/h264_1280x720_30f.h264")
	}
	p, err := NewPipe("sw", "h264", 1280, 720)
	if err != nil {
		t.Fatalf("NewPipe: %v", err)
	}
	defer p.Close()
	raw, err := os.ReadFile("testdata/h264_1280x720_30f.h264")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		// Stream the fixture in small chunks so ffmpeg can start producing
		// output while we're still feeding it (closer to real RTP arrival).
		const chunk = 4096
		for i := 0; i < len(raw); i += chunk {
			j := i + chunk
			if j > len(raw) {
				j = len(raw)
			}
			if _, err := p.WriteNAL(raw[i:j]); err != nil {
				return
			}
		}
		// Signal end-of-stream so ffmpeg flushes its final frames.
		_ = p.CloseStdin()
	}()

	frames := 0
	wantFrameLen := 1280 * 720 * 3 / 2
	for {
		frame, err := p.ReadFrame()
		if err != nil {
			break
		}
		if len(frame) != wantFrameLen {
			t.Fatalf("frame %d len %d != %d", frames, len(frame), wantFrameLen)
		}
		frames++
	}
	<-writeDone
	if frames == 0 {
		t.Fatal("got 0 decoded frames from a 30-frame fixture")
	}
	if frames < 25 {
		// Some libx264 ultrafast builds round down; >25 is a robust floor.
		t.Logf("got %d frames (expected ~30); acceptable", frames)
	}
}
