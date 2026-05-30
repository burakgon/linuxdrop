// Package tray shows a StatusNotifierItem in the KDE/Wayland system tray via
// fyne.io/systray (pure DBus, no GTK). It reflects connection state and offers
// pause/resume, show-pairing-QR and quit.
package tray

import (
	"strings"
	"sync/atomic"

	"fyne.io/systray"
)

type Callbacks struct {
	OnQuit        func()
	OnTogglePause func(paused bool)
	OnShowQR      func()
	OnSendFile    func()
	OnOpenFolder  func()
	// Webcam: phone-as-webcam. camera ∈ {"back","front"}; resolution ∈ {"720p","1080p"}.
	OnStartWebcam func(camera, resolution string)
	OnStopWebcam  func()
}

type Tray struct {
	cb            Callbacks
	statusItem    *systray.MenuItem
	pauseItem     *systray.MenuItem
	camStop       *systray.MenuItem
	camStart720B  *systray.MenuItem
	camStart1080B *systray.MenuItem
	camStartFront *systray.MenuItem
	connected     atomic.Bool
	paused        atomic.Bool
	webcamActive  atomic.Bool
	ready         atomic.Bool
}

func New(cb Callbacks) *Tray { return &Tray{cb: cb} }

// Run owns the tray event loop and blocks until Quit.
func (t *Tray) Run() { systray.Run(t.onReady, func() {}) }

func (t *Tray) Quit() { systray.Quit() }

func (t *Tray) SetConnected(connected bool) {
	t.connected.Store(connected)
	t.refresh()
}

// SetDevices updates the tooltip with the connected device labels.
func (t *Tray) SetDevices(devices []string) {
	if !t.ready.Load() {
		return
	}
	if len(devices) == 0 {
		systray.SetTooltip("LinuxDrop — clipboard sync")
	} else {
		systray.SetTooltip("LinuxDrop — " + strings.Join(devices, ", "))
	}
}

func (t *Tray) onReady() {
	systray.SetTitle("LinuxDrop")
	systray.SetTooltip("LinuxDrop — clipboard sync")
	systray.SetIcon(iconPNG(false))

	t.statusItem = systray.AddMenuItem("Status: connecting…", "")
	t.statusItem.Disable()
	systray.AddSeparator()
	t.pauseItem = systray.AddMenuItem("Pause", "Pause syncing")
	systray.AddSeparator()
	sendItem := systray.AddMenuItem("Send file…", "Send a file directly to a device")
	openItem := systray.AddMenuItem("Open received files", "Open the Downloads folder")
	systray.AddSeparator()

	// Phone-as-webcam submenu.
	camMenu := systray.AddMenuItem("Use phone camera", "Stream your phone's camera as a virtual webcam")
	t.camStart720B = camMenu.AddSubMenuItem("Start (720p, back)", "")
	t.camStart1080B = camMenu.AddSubMenuItem("Start (1080p, back)", "")
	t.camStartFront = camMenu.AddSubMenuItem("Start (720p, front)", "")
	t.camStop = camMenu.AddSubMenuItem("Stop streaming", "")
	t.camStop.Disable()

	systray.AddSeparator()
	qrItem := systray.AddMenuItem("Show pairing QR", "Pairing QR")
	systray.AddSeparator()
	quitItem := systray.AddMenuItem("Quit", "Stop the daemon")

	t.ready.Store(true)
	t.refresh()

	go func() {
		for {
			select {
			case <-t.pauseItem.ClickedCh:
				p := !t.paused.Load()
				t.paused.Store(p)
				if t.cb.OnTogglePause != nil {
					t.cb.OnTogglePause(p)
				}
				t.refresh()
			case <-sendItem.ClickedCh:
				if t.cb.OnSendFile != nil {
					go t.cb.OnSendFile()
				}
			case <-openItem.ClickedCh:
				if t.cb.OnOpenFolder != nil {
					go t.cb.OnOpenFolder()
				}
			case <-qrItem.ClickedCh:
				if t.cb.OnShowQR != nil {
					t.cb.OnShowQR()
				}
			case <-t.camStart720B.ClickedCh:
				if t.cb.OnStartWebcam != nil {
					go t.cb.OnStartWebcam("back", "720p")
				}
			case <-t.camStart1080B.ClickedCh:
				if t.cb.OnStartWebcam != nil {
					go t.cb.OnStartWebcam("back", "1080p")
				}
			case <-t.camStartFront.ClickedCh:
				if t.cb.OnStartWebcam != nil {
					go t.cb.OnStartWebcam("front", "720p")
				}
			case <-t.camStop.ClickedCh:
				if t.cb.OnStopWebcam != nil {
					go t.cb.OnStopWebcam()
				}
			case <-quitItem.ClickedCh:
				if t.cb.OnQuit != nil {
					t.cb.OnQuit()
				}
			}
		}
	}()
}

// SetWebcamActive flips the tray indicator + Stop item enable state. Safe to
// call before the tray is ready (no-op until onReady runs).
func (t *Tray) SetWebcamActive(active bool) {
	t.webcamActive.Store(active)
	if !t.ready.Load() {
		return
	}
	if t.camStop != nil {
		if active {
			t.camStop.Enable()
			t.camStart720B.Disable()
			t.camStart1080B.Disable()
			t.camStartFront.Disable()
			systray.SetTitle("LinuxDrop · 📹")
		} else {
			t.camStop.Disable()
			t.camStart720B.Enable()
			t.camStart1080B.Enable()
			t.camStartFront.Enable()
			systray.SetTitle("LinuxDrop")
		}
	}
}

func (t *Tray) refresh() {
	if !t.ready.Load() || t.statusItem == nil {
		return
	}
	status := "offline"
	if t.connected.Load() {
		status = "connected"
	}
	if t.paused.Load() {
		status = "paused"
	}
	t.statusItem.SetTitle("Status: " + status)
	if t.paused.Load() {
		t.pauseItem.SetTitle("Resume")
	} else {
		t.pauseItem.SetTitle("Pause")
	}
	systray.SetIcon(iconPNG(t.connected.Load() && !t.paused.Load()))
}
