package tether

import (
	"context"
	"log"
	"sync"
	"time"

	"linuxdrop/linux/internal/crypto"
)

// Orchestrator brings the box online via the phone's hotspot and tears it down. It owns the BLE
// central, the wifi connector, and the keepalive loop. Safe for concurrent On/Off.
type Orchestrator struct {
	log    *log.Logger
	ble    *BLECentral
	wifi   *WifiConnector
	secret []byte

	mu       sync.Mutex
	seq      uint32
	tethered bool
	stopKeep chan struct{}
	onState  func(tethered bool, detail string)
}

func NewOrchestrator(secret []byte, logger *log.Logger) *Orchestrator {
	return &Orchestrator{
		log:    logger,
		ble:    NewBLECentral(crypto.TetherBLEKey(secret)),
		wifi:   NewWifiConnector(crypto.TetherSSID(secret), crypto.TetherPSK(secret)),
		secret: secret,
	}
}

// SetOnState registers a callback for tray/UI updates.
func (o *Orchestrator) SetOnState(f func(tethered bool, detail string)) { o.onState = f }

func (o *Orchestrator) emit(tethered bool, detail string) {
	if o.onState != nil {
		o.onState(tethered, detail)
	}
}

// On: BLE-wake the phone, join the hotspot, verify online, start keepalive.
func (o *Orchestrator) On(ctx context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.tethered {
		return nil
	}
	o.emit(false, "waking phone over BLE…")
	o.seq++
	res, err := o.ble.Command(OpEnable, o.seq)
	if err != nil {
		o.emit(false, "phone unreachable over BLE")
		return err
	}
	if res != 0 {
		o.emit(false, "phone could not start the hotspot")
		return &resultErr{res}
	}
	o.emit(false, "joining "+o.wifi.ssid+"…")
	if err := o.wifi.Join(ctx); err != nil {
		o.emit(false, "couldn't join the hotspot")
		return err
	}
	if !IsOnline(ctx) {
		o.emit(false, "joined hotspot but still offline (phone has no data?)")
		o.wifi.Leave()
		return &offlineErr{}
	}
	o.tethered = true
	o.stopKeep = make(chan struct{})
	go o.keepalive(o.stopKeep)
	o.emit(true, "internet via phone ("+o.wifi.ssid+")")
	o.log.Printf("tether: up via %s", o.wifi.ssid)
	return nil
}

// Off: stop the keepalive (if running), tell the phone to stop, and leave the hotspot. Safe to call
// on a fresh process (CLI `tether off`) where we never started a keepalive.
func (o *Orchestrator) Off() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.stopKeep != nil {
		close(o.stopKeep)
		o.stopKeep = nil
	}
	o.seq++
	o.ble.Command(OpDisable, o.seq) // best-effort; idempotent on the phone
	o.wifi.Leave()
	o.tethered = false
	o.emit(false, "off")
	o.log.Printf("tether: down")
}

func (o *Orchestrator) Tethered() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.tethered
}

// UsingTether reports whether the active wifi connection is our hotspot profile.
func (o *Orchestrator) UsingTether() bool { return o.wifi.Active() }

func (o *Orchestrator) keepalive(stop <-chan struct{}) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			o.mu.Lock()
			o.seq++
			seq := o.seq
			o.mu.Unlock()
			o.ble.Command(OpKeepAlive, seq)
		}
	}
}

type resultErr struct{ code byte }

func (e *resultErr) Error() string { return "tether: phone returned error code" }

type offlineErr struct{}

func (e *offlineErr) Error() string { return "tether: joined hotspot but still offline" }
