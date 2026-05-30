// Package v4l2 is a minimal write-only wrapper over a v4l2loopback output device.
// We set the pixel format once (YUV420 / I420) then write frames as raw byte
// slices of size w*h*3/2. The kernel module handles the queue and exposes the
// frames to userspace readers (Zoom/Chrome/OBS) on the same /dev/video*.
package v4l2

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// v4l2_format struct (struct v4l2_format from videodev2.h). We only touch the
// pix union member, but the kernel reads the whole 208-byte struct, so we pad
// the rest. Layout: type(4) + pad(4) + 200 bytes of fmt union.
type v4l2Format struct {
	Type uint32
	_    [4]byte // padding before the fmt union (kernel alignment)
	Pix  v4l2PixFormat
	_    [200 - unsafe.Sizeof(v4l2PixFormat{})]byte
}

// v4l2_pix_format from videodev2.h (subset we care about).
type v4l2PixFormat struct {
	Width        uint32
	Height       uint32
	Pixelformat  uint32 // FourCC
	Field        uint32
	BytesPerLine uint32
	SizeImage    uint32
	Colorspace   uint32
	Priv         uint32
	Flags        uint32
	YcbcrEnc     uint32
	Quantization uint32
	XferFunc     uint32
}

const (
	v4l2BufTypeVideoOutput = 2 // V4L2_BUF_TYPE_VIDEO_OUTPUT
	v4l2FieldNone          = 1 // V4L2_FIELD_NONE
	v4l2ColorspaceREC709   = 3 // V4L2_COLORSPACE_REC709 — sensible HD default
)

// FourCC for YUV420 planar (I420): 'Y' | 'U'<<8 | '1'<<16 | '2'<<24
const pixFmtYUV420 = uint32('Y') | uint32('U')<<8 | uint32('1')<<16 | uint32('2')<<24

// VIDIOC_S_FMT = _IOWR('V', 5, struct v4l2_format)
// _IOC encoding: (dir<<30) | (size<<16) | (type<<8) | nr   ; size = sizeof = 208
const vidiocSFmt = (uint(3) << 30) | (uint(208) << 16) | (uint('V') << 8) | 5

// Writer wraps an open file descriptor for a v4l2loopback output device.
// Not safe for concurrent Write — serialize externally.
type Writer struct {
	f    *os.File
	w, h int
}

// frameSize returns the byte size of one YUV420 frame at the given resolution.
func frameSize(w, h int) int { return w * h * 3 / 2 }

// Open opens the v4l2loopback device at path, sets the pixel format to YUV420
// at the requested resolution + frame rate, and returns a ready-to-Write Writer.
// fps is recorded for diagnostic purposes; the kernel doesn't require it for
// output devices (the producer dictates the cadence).
//
// We open the device O_NONBLOCK so write() never blocks when no reader is
// consuming. With v4l2loopback's default behavior (and exclusive_caps=1), a
// blocking write would freeze the producer the moment a frame has nowhere to
// go — which means the producer must wait for a reader before it can move at
// all. With O_NONBLOCK, the kernel returns EAGAIN instead, and we simply drop
// the frame (logged once below). That way a user can open Cheese / Zoom AFTER
// the stream is already running and start seeing live frames immediately.
func Open(path string, w, h, fps int) (*Writer, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	fmtStruct := v4l2Format{
		Type: v4l2BufTypeVideoOutput,
		Pix: v4l2PixFormat{
			Width:        uint32(w),
			Height:       uint32(h),
			Pixelformat:  pixFmtYUV420,
			Field:        v4l2FieldNone,
			BytesPerLine: uint32(w),
			SizeImage:    uint32(frameSize(w, h)),
			Colorspace:   v4l2ColorspaceREC709,
		},
	}
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, f.Fd(), uintptr(vidiocSFmt), uintptr(unsafe.Pointer(&fmtStruct))); errno != 0 {
		f.Close()
		return nil, fmt.Errorf("VIDIOC_S_FMT %dx%d on %s: %w", w, h, path, errnoFor(errno))
	}
	return &Writer{f: f, w: w, h: h}, nil
}

// Write streams one YUV420 frame. buf must be exactly frameSize(w,h) bytes; we
// return an error otherwise so callers don't silently lose framing.
// When no v4l2 reader is consuming and v4l2loopback has no buffer slot free,
// write() returns EAGAIN under O_NONBLOCK — we treat that as a drop and return
// nil so the producer keeps running.
func (w *Writer) Write(buf []byte) error {
	if want := frameSize(w.w, w.h); len(buf) != want {
		return fmt.Errorf("frame size %d != expected %d (%dx%d YUV420)", len(buf), want, w.w, w.h)
	}
	_, err := w.f.Write(buf)
	if err != nil && errors.Is(err, unix.EAGAIN) {
		return nil // dropped — no reader / queue full
	}
	return err
}

// Close releases the device. After Close, v4l2loopback exposes a blank stream
// until the next Writer opens it.
func (w *Writer) Close() error { return w.f.Close() }

// errnoFor wraps a raw errno so callers can errors.Is against fs/syscall sentinels.
func errnoFor(e syscall.Errno) error { return os.NewSyscallError("ioctl", e) }
