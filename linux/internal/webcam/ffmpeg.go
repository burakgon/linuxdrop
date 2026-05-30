package webcam

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
)

// probeHWAccel picks the first hardware decoder that should work on this box.
// Order: VAAPI → NVDEC → SW. LINUXDROP_HWACCEL pins the choice. We do a CHEAP
// probe here (device-node / nvidia-smi presence). The actual "can we open it"
// test happens in NewPipe; if it fails we transparently fall back to SW.
func probeHWAccel() string {
	if v := os.Getenv("LINUXDROP_HWACCEL"); v != "" {
		return v
	}
	if _, err := os.Stat("/dev/dri/renderD128"); err == nil {
		return "vaapi"
	}
	if _, err := exec.LookPath("nvidia-smi"); err == nil {
		if err := exec.Command("nvidia-smi", "-L").Run(); err == nil {
			return "cuda"
		}
	}
	return "sw"
}

// ffmpegArgs builds the argv for an ffmpeg subprocess that decodes a NALU stream
// from stdin and writes YUV420 raw video to stdout. The HW path keeps the format
// in CPU memory (yuv420p output) so we can write it directly to v4l2loopback.
func ffmpegArgs(hwaccel, codec string, w, h int) []string {
	// -loglevel: keep warnings; suppress info chatter.
	// (-fflags +nobuffer was tested and breaks the H.264 parser on piped input;
	// ffmpeg's default demuxer buffering is needed to assemble NALUs reliably.)
	args := []string{"-loglevel", "warning"}
	switch hwaccel {
	case "vaapi":
		args = append(args,
			"-hwaccel", "vaapi",
			"-hwaccel_device", "/dev/dri/renderD128",
			"-hwaccel_output_format", "yuv420p",
		)
	case "cuda":
		args = append(args,
			"-hwaccel", "cuda",
			"-hwaccel_output_format", "yuv420p",
		)
	case "qsv":
		args = append(args, "-hwaccel", "qsv", "-hwaccel_output_format", "yuv420p")
	case "sw", "":
		// no -hwaccel flag
	}
	// Input: raw Annex-B NALU stream of the given codec
	args = append(args, "-f", codec, "-i", "pipe:0")
	// Output: raw YUV420 at the input size
	args = append(args, "-pix_fmt", "yuv420p", "-f", "rawvideo", "pipe:1")
	return args
}

// Pipe is a long-lived ffmpeg subprocess. WriteNAL feeds encoded data;
// ReadFrame returns one YUV420 frame (size = w*h*3/2). Close terminates.
type Pipe struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	w, h    int
	frameSz int
	codec   string
	hwaccel string
}

// NewPipe starts ffmpeg with the chosen HW path. On open failure of the HW
// backend, falls back to SW automatically (logged). Caller passes
// codec ∈ {"h264","hevc"}.
func NewPipe(hwaccel, codec string, w, h int) (*Pipe, error) {
	if codec != "h264" && codec != "hevc" {
		return nil, fmt.Errorf("unsupported codec %q (want h264 or hevc)", codec)
	}
	p, err := startPipe(hwaccel, codec, w, h)
	if err != nil && hwaccel != "sw" {
		log.Printf("webcam: HW accel %q failed (%v); falling back to SW", hwaccel, err)
		return startPipe("sw", codec, w, h)
	}
	return p, err
}

func startPipe(hwaccel, codec string, w, h int) (*Pipe, error) {
	cmd := exec.Command("ffmpeg", ffmpegArgs(hwaccel, codec, w, h)...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg stdout: %w", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("ffmpeg start: %w", err)
	}
	return &Pipe{
		cmd:     cmd,
		stdin:   stdin,
		stdout:  bufio.NewReaderSize(stdout, w*h*3),
		w:       w,
		h:       h,
		frameSz: w * h * 3 / 2,
		codec:   codec,
		hwaccel: hwaccel,
	}, nil
}

// WriteNAL feeds an Annex-B NALU (or a buffer of multiple NALUs) to ffmpeg.
func (p *Pipe) WriteNAL(buf []byte) (int, error) { return p.stdin.Write(buf) }

// CloseStdin signals end-of-stream to ffmpeg (it will then flush all queued frames).
func (p *Pipe) CloseStdin() error { return p.stdin.Close() }

// ReadFrame returns exactly one YUV420 frame. Returns io.EOF only when ffmpeg exits.
func (p *Pipe) ReadFrame() ([]byte, error) {
	frame := make([]byte, p.frameSz)
	if _, err := io.ReadFull(p.stdout, frame); err != nil {
		return nil, err
	}
	return frame, nil
}

// HWAccel returns the actual hardware path in use after fallback.
func (p *Pipe) HWAccel() string { return p.hwaccel }

// Close terminates the ffmpeg subprocess.
func (p *Pipe) Close() error {
	_ = p.stdin.Close()
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	return p.cmd.Wait()
}
