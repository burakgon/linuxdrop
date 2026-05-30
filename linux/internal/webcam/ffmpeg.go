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
// from stdin and writes YUV420 raw video to stdout (used by tests + the legacy
// pipe path). The HW path keeps the format in CPU memory (yuv420p output).
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
		args = append(args,
			"-hwaccel", "qsv",
			"-hwaccel_output_format", "yuv420p",
		)
	case "sw", "":
		// no -hwaccel flag
	}
	// Input: raw Annex-B NALU stream of the given codec
	args = append(args, "-f", codec, "-i", "pipe:0")
	// Output: enforce planar I420 (YU12) in CPU memory. The `format=yuv420p`
	// filter is cheap and idempotent — it does nothing when the source is
	// already I420 (the common case after -hwaccel_output_format yuv420p)
	// and converts when it isn't. Without this safety net, VAAPI sometimes
	// returns NV12 (semi-planar interleaved UV); writing those bytes to a
	// YU12 (planar) v4l2loopback device swaps U/V → wrong colours.
	args = append(args,
		"-vf", "format=yuv420p",
		"-pix_fmt", "yuv420p",
		"-f", "rawvideo", "pipe:1",
	)
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

// NewV4L2Writer starts ffmpeg as: stdin → decode (HW or SW) → write directly
// to a v4l2loopback device. Letting ffmpeg own the v4l2 negotiation avoids
// every plane-order / colorspace bug a hand-rolled writer can introduce —
// ffmpeg consults the device's VIDIOC_ENUM_FMT and picks a compatible
// pixel format. The returned Pipe has a working WriteNAL/Close; ReadFrame
// returns 0 bytes (no stdout output).
func NewV4L2Writer(hwaccel, codec string, w, h int, devPath string) (*Pipe, error) {
	if codec != "h264" && codec != "hevc" {
		return nil, fmt.Errorf("unsupported codec %q (want h264 or hevc)", codec)
	}
	p, err := startV4L2(hwaccel, codec, w, h, devPath)
	if err != nil && hwaccel != "sw" {
		log.Printf("webcam: HW accel %q failed (%v); falling back to SW", hwaccel, err)
		return startV4L2("sw", codec, w, h, devPath)
	}
	return p, err
}

// ffmpegV4L2Args builds argv for: NALU stream on stdin → v4l2loopback output.
func ffmpegV4L2Args(hwaccel, codec string, w, h int, devPath string) []string {
	args := []string{"-loglevel", "warning"}
	switch hwaccel {
	case "vaapi":
		args = append(args,
			"-hwaccel", "vaapi",
			"-hwaccel_device", "/dev/dri/renderD128",
			"-hwaccel_output_format", "yuv420p",
		)
	case "cuda":
		args = append(args, "-hwaccel", "cuda", "-hwaccel_output_format", "yuv420p")
	case "qsv":
		args = append(args, "-hwaccel", "qsv", "-hwaccel_output_format", "yuv420p")
	}
	args = append(args, "-f", codec, "-i", "pipe:0")
	// v4l2loopback consumer. The Android back camera (when phone is held in
	// portrait) sends 720x1280, so we transpose 90° CW + scale to the target
	// landscape dimensions. format=yuv420p forces planar I420 (avoids the
	// NV12-as-I420 plane-swap-tinted-output bug).
	vf := fmt.Sprintf("transpose=1,scale=%d:%d,format=yuv420p", w, h)
	args = append(args,
		"-vf", vf,
		"-s", fmt.Sprintf("%dx%d", w, h),
		"-pix_fmt", "yuv420p",
		"-f", "v4l2",
		devPath,
	)
	return args
}

func startV4L2(hwaccel, codec string, w, h int, devPath string) (*Pipe, error) {
	cmd := exec.Command("ffmpeg", ffmpegV4L2Args(hwaccel, codec, w, h, devPath)...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg stdin: %w", err)
	}
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stderr // no output to read; just log
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("ffmpeg start: %w", err)
	}
	return &Pipe{
		cmd:     cmd,
		stdin:   stdin,
		stdout:  nil, // unused in v4l2-out mode
		w:       w,
		h:       h,
		frameSz: w * h * 3 / 2,
		codec:   codec,
		hwaccel: hwaccel,
	}, nil
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
// Returns io.EOF immediately when the pipe is in v4l2-out mode (stdout unused).
func (p *Pipe) ReadFrame() ([]byte, error) {
	if p.stdout == nil {
		return nil, io.EOF
	}
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
