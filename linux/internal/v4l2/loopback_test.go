package v4l2

import (
	"errors"
	"io/fs"
	"os"
	"testing"
)

func TestFrameSize(t *testing.T) {
	if got, want := frameSize(1280, 720), 1280*720*3/2; got != want {
		t.Fatalf("frameSize(1280,720)=%d, want %d", got, want)
	}
	if got, want := frameSize(1920, 1080), 1920*1080*3/2; got != want {
		t.Fatalf("frameSize(1920,1080)=%d, want %d", got, want)
	}
}

func TestOpen_MissingDeviceReturnsError(t *testing.T) {
	_, err := Open("/does/not/exist/video99", 1280, 720, 30)
	if err == nil {
		t.Fatal("expected error opening missing device")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected fs.ErrNotExist, got %v", err)
	}
}

// Integration: requires a real /dev/videoNN provisioned by `linuxdropd webcam install`.
// Set V4L2_TEST_DEVICE=/dev/video20 to run it.
func TestWrite_RealDevice(t *testing.T) {
	dev := os.Getenv("V4L2_TEST_DEVICE")
	if dev == "" {
		t.Skip("V4L2_TEST_DEVICE not set; skipping live device write test")
	}
	w, err := Open(dev, 320, 240, 15)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer w.Close()
	frame := make([]byte, frameSize(320, 240))
	for i := range frame {
		frame[i] = byte(i)
	}
	if err := w.Write(frame); err != nil {
		t.Fatalf("Write: %v", err)
	}
}
