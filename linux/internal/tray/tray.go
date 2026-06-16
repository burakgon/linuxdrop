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
	OnQuit         func()
	OnTogglePause  func(paused bool)
	OnToggleTether func(on bool)
	OnShowQR       func()
	OnSendFile     func()
	OnOpenFolder   func()
}

type Tray struct {
	cb           Callbacks
	statusItem   *systray.MenuItem
	pauseItem    *systray.MenuItem
	tetherItem   *systray.MenuItem
	connected    atomic.Bool
	paused       atomic.Bool
	ready        atomic.Bool
	tetherOn     atomic.Bool
	tetherDetail atomic.Value // string
}

func New(cb Callbacks) *Tray { return &Tray{cb: cb} }

// Run owns the tray event loop and blocks until Quit.
func (t *Tray) Run() { systray.Run(t.onReady, func() {}) }

func (t *Tray) Quit() { systray.Quit() }

func (t *Tray) SetConnected(connected bool) {
	t.connected.Store(connected)
	t.refresh()
}

// SetTether updates the tether line ("on/off" + a one-line detail). Called by the orchestrator.
func (t *Tray) SetTether(on bool, detail string) {
	t.tetherOn.Store(on)
	t.tetherDetail.Store(detail)
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
	t.tetherItem = systray.AddMenuItem("Phone internet: off", "Use the phone's hotspot when this box is offline")
	systray.AddSeparator()
	sendItem := systray.AddMenuItem("Send file…", "Send a file directly to a device")
	openItem := systray.AddMenuItem("Open received files", "Open the Downloads folder")
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
			case <-t.tetherItem.ClickedCh:
				on := !t.tetherOn.Load()
				t.tetherOn.Store(on)
				if t.cb.OnToggleTether != nil {
					go t.cb.OnToggleTether(on)
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
			case <-quitItem.ClickedCh:
				if t.cb.OnQuit != nil {
					t.cb.OnQuit()
				}
			}
		}
	}()
}

func (t *Tray) refresh() {
	if !t.ready.Load() || t.statusItem == nil {
		return
	}
	sym := "○ offline"
	if t.connected.Load() {
		sym = "● connected"
	}
	if t.paused.Load() {
		sym = "⏸ paused"
	}
	t.statusItem.SetTitle("Clipboard sync: " + sym)
	if t.paused.Load() {
		t.pauseItem.SetTitle("Resume")
	} else {
		t.pauseItem.SetTitle("Pause")
	}
	if t.tetherItem != nil {
		label := "Phone internet: off"
		if t.tetherOn.Load() {
			label = "Phone internet: ON"
		}
		if d, _ := t.tetherDetail.Load().(string); d != "" {
			label += " — " + d
		}
		t.tetherItem.SetTitle(label)
	}
	systray.SetIcon(iconPNG(t.connected.Load() && !t.paused.Load()))
}
