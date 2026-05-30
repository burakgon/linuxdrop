// Command linuxdropd is the Linux clipboard-sync daemon.
//
//	linuxdropd gen-secret                 print a new random secret (hex)
//	linuxdropd pair <uri|hex> [relay]     store secret (+relay) for this device
//	linuxdropd qr                         print the pairing URI + QR (terminal)
//	linuxdropd send <file> [device]       send a file directly (P2P) to a peer
//	linuxdropd [run] [--relay U] [--dev-secret HEX] [--no-tray]
//	                                       watch + sync (system tray by default)
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mdp/qrterminal/v3"
	"rsc.io/qr"

	"linuxdrop/linux/internal/blob"
	"linuxdrop/linux/internal/clipboard"
	"linuxdrop/linux/internal/config"
	"linuxdrop/linux/internal/crypto"
	"linuxdrop/linux/internal/engine"
	"linuxdrop/linux/internal/p2p"
	"linuxdrop/linux/internal/tray"
	"linuxdrop/linux/internal/wire"
	"linuxdrop/linux/internal/ws"
)

func main() {
	logger := log.New(os.Stderr, "linuxdrop ", log.LstdFlags|log.Lmsgprefix)

	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "gen-secret":
			fmt.Println(config.HexSecret(config.GenerateSecret()))
		case "pair":
			cmdPair(logger, args[1:])
		case "qr":
			cmdQR(logger)
		case "send":
			cmdSend(logger, args[1:])
		case "run":
			cmdRun(logger, args[1:])
		default:
			logger.Fatalf("unknown command %q (use: gen-secret | pair | qr | send | run)", args[0])
		}
		return
	}
	cmdRun(logger, args)
}

func cmdPair(logger *log.Logger, args []string) {
	if len(args) < 1 {
		logger.Fatal("usage: linuxdropd pair <linuxdrop://... | hex> [relay-url]")
	}
	secret, relay, err := config.ParsePairing(args[0])
	if err != nil {
		logger.Fatalf("pair: %v", err)
	}
	if len(args) >= 2 {
		relay = args[1]
	}
	cfg, err := config.Load()
	if err != nil {
		logger.Fatalf("config: %v", err)
	}
	if relay == "" {
		relay = cfg.RelayURL // keep an already-configured relay if the input had none
	}
	if relay == "" {
		logger.Fatal("no relay URL — pair with a linuxdrop:// URI that includes one, or: linuxdropd pair <hex> <wss://your-relay>")
	}
	if err := config.SaveSecret(config.HexSecret(secret)); err != nil {
		logger.Fatalf("save secret: %v", err)
	}
	cfg.RelayURL = relay
	if err := cfg.Save(); err != nil {
		logger.Fatalf("save config: %v", err)
	}
	logger.Printf("paired: room=%s relay=%s", crypto.RoomID(secret), cfg.RelayURL)
}

func cmdQR(logger *log.Logger) {
	cfg, _ := config.Load()
	if cfg.RelayURL == "" {
		logger.Fatal("no relay configured — set one first: linuxdropd pair <hex> <wss://your-relay>")
	}
	hexS, _ := config.LoadSecret()
	if hexS == "" {
		// No network yet: create one so `qr` always shows something to scan.
		hexS = config.HexSecret(config.GenerateSecret())
		if err := config.SaveSecret(hexS); err != nil {
			logger.Fatalf("save secret: %v", err)
		}
		logger.Println("created a new sync network")
	}
	secret, err := config.DecodeSecretHex(hexS)
	if err != nil {
		logger.Fatalf("stored secret invalid: %v", err)
	}
	uri := config.PairingURI(secret, cfg.RelayURL)
	fmt.Println("\nScan this on your phone with “Scan QR · Join network”:")
	fmt.Println(uri)
	fmt.Println()
	qrterminal.GenerateHalfBlock(uri, qrterminal.M, os.Stdout)
}

func cmdRun(logger *log.Logger, args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	relayFlag := fs.String("relay", "", "override relay URL (ws:// or wss://)")
	devSecret := fs.String("dev-secret", "", "override shared secret (hex, development)")
	noTray := fs.Bool("no-tray", false, "run without the system tray")
	_ = fs.Parse(args)

	cfg, err := config.Load()
	if err != nil {
		logger.Fatalf("config: %v", err)
	}

	relay := cfg.RelayURL
	if *relayFlag != "" {
		relay = *relayFlag
	}

	var secret []byte
	if *devSecret != "" {
		if secret, err = config.DecodeSecretHex(*devSecret); err != nil {
			logger.Fatalf("bad --dev-secret: %v", err)
		}
	} else {
		hexS, _ := config.LoadSecret()
		if hexS == "" {
			logger.Fatal("not paired; run: linuxdropd pair <uri|hex> [relay]  (or pass --dev-secret)")
		}
		if secret, err = config.DecodeSecretHex(hexS); err != nil {
			logger.Fatalf("stored secret invalid: %v", err)
		}
	}
	if relay == "" {
		logger.Fatal("no relay URL; pair with a linuxdrop:// URI or pass --relay")
	}

	cipher, err := crypto.NewCipher(secret)
	if err != nil {
		logger.Fatalf("cipher: %v", err)
	}
	roomID := crypto.RoomID(secret)
	dev := cfg.DeviceID

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var tr *tray.Tray
	helloEnc := buildHelloEnc(cipher, cfg.DeviceName)
	client := ws.NewClient(relay, roomID, dev, helloEnc, logger, func(connected bool) {
		if tr != nil {
			tr.SetConnected(connected)
		}
	})
	eng := engine.New(cipher, clipboard.NewWayland(), client, dev, roomID, blob.NewClient(relay), logger)
	eng.SetOnRoster(func(devices []string) {
		if tr != nil {
			tr.SetDevices(devices)
		}
	})

	// Direct P2P file receiver (auto-accept → Downloads). Signaling rides the relay;
	// the file bytes go straight peer-to-peer over WebRTC.
	p2pMgr := p2p.NewManager(dev, relay, downloadsDir(), logger)
	p2pMgr.SetSendSignal(eng.SendSignal)
	eng.SetOnSignal(p2pMgr.OnSignal)
	p2pMgr.SetOnReceived(func(path string) {
		notify("LinuxDrop", "Received "+filepath.Base(path))
		_ = exec.Command("xdg-open", filepath.Dir(path)).Start() // reveal the folder
	})

	go func() {
		if err := eng.Run(ctx); err != nil && ctx.Err() == nil {
			logger.Printf("engine: %v", err)
		}
		stop()
	}()

	logger.Printf("running: room=%s dev=%s relay=%s", roomID, dev, relay)

	if *noTray {
		<-ctx.Done()
		logger.Println("shutting down")
		return
	}

	tr = tray.New(tray.Callbacks{
		OnQuit: func() { stop() },
		OnTogglePause: func(p bool) {
			eng.SetPaused(p)
			if p {
				logger.Println("paused")
			} else {
				logger.Println("resumed")
			}
		},
		OnShowQR:     func() { showPairingQR(logger, secret, relay) },
		OnOpenFolder: func() { _ = exec.Command("xdg-open", downloadsDir()).Start() },
		OnSendFile: func() {
			path, ok := pickFile()
			if !ok {
				return
			}
			dev, ok := pickDevice(eng.Peers())
			if !ok {
				return
			}
			logger.Printf("sending %q to %s…", filepath.Base(path), dev)
			if err := p2pMgr.SendFile(dev, path); err != nil {
				logger.Printf("send failed: %v", err)
				notify("LinuxDrop", "Failed to send "+filepath.Base(path))
			} else {
				notify("LinuxDrop", "Sent "+filepath.Base(path))
			}
		},
	})
	go func() {
		<-ctx.Done()
		tr.Quit()
	}()
	tr.Run() // blocks until quit
	logger.Println("shutting down")
}

// buildHelloEnc seals this device's {name, platform} for the relay roster so
// peers can show our name (the relay only ever sees the ciphertext).
func buildHelloEnc(c *crypto.Cipher, name string) *wire.Enc {
	pt, err := json.Marshal(map[string]string{"name": name, "platform": "linux"})
	if err != nil {
		return nil
	}
	iv, ct, err := c.Seal(pt)
	if err != nil {
		return nil
	}
	return &wire.Enc{V: 1, Alg: "AES-256-GCM", IV: iv, Ct: ct}
}

// showPairingQR writes the pairing QR to a temp PNG and opens it with the
// desktop's default image viewer.
func showPairingQR(logger *log.Logger, secret []byte, relay string) {
	uri := config.PairingURI(secret, relay)
	code, err := qr.Encode(uri, qr.M)
	if err != nil {
		logger.Printf("qr encode: %v", err)
		return
	}
	path := filepath.Join(os.TempDir(), "linuxdrop-pair.png")
	if err := os.WriteFile(path, code.PNG(), 0o600); err != nil {
		logger.Printf("qr write: %v", err)
		return
	}
	_ = exec.Command("xdg-open", path).Start()
	logger.Printf("pairing: %s", uri)
}

// cmdSend connects to the relay, finds the target peer in the roster, and streams a
// file to it directly over WebRTC (the relay carries only signaling). Exits on done.
func cmdSend(logger *log.Logger, args []string) {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	relayFlag := fs.String("relay", "", "override relay URL (ws:// or wss://)")
	devSecret := fs.String("dev-secret", "", "override shared secret (hex, development)")
	toFlag := fs.String("to", "", "target device (name or id); otherwise auto/picker")
	_ = fs.Parse(args)
	files := fs.Args()
	if len(files) < 1 {
		logger.Fatal("usage: linuxdropd send [--relay U] [--dev-secret H] [--to DEVICE] <file>...")
	}
	for _, f := range files {
		if _, err := os.Stat(f); err != nil {
			logger.Fatalf("file %q: %v", f, err)
		}
	}
	want := *toFlag

	cfg, err := config.Load()
	if err != nil {
		logger.Fatalf("config: %v", err)
	}
	relay := cfg.RelayURL
	if *relayFlag != "" {
		relay = *relayFlag
	}
	if relay == "" {
		logger.Fatal("no relay configured; pair first or pass --relay")
	}

	var secret []byte
	if *devSecret != "" {
		if secret, err = config.DecodeSecretHex(*devSecret); err != nil {
			logger.Fatalf("bad --dev-secret: %v", err)
		}
	} else {
		hexS, _ := config.LoadSecret()
		if hexS == "" {
			logger.Fatal("not paired; run: linuxdropd pair <uri|hex> <relay>")
		}
		if secret, err = config.DecodeSecretHex(hexS); err != nil {
			logger.Fatalf("stored secret invalid: %v", err)
		}
	}
	cipher, err := crypto.NewCipher(secret)
	if err != nil {
		logger.Fatalf("cipher: %v", err)
	}
	roomID := crypto.RoomID(secret)
	dev := "linux-send-" + wire.GenID()[:6]

	mgr := p2p.NewManager(dev, relay, downloadsDir(), logger)
	client := ws.NewClient(relay, roomID, dev, buildHelloEnc(cipher, "linuxdrop (sender)"), logger, func(bool) {})
	mgr.SetSendSignal(func(to string, payload []byte) {
		iv, ct, err := cipher.Seal(payload)
		if err != nil {
			return
		}
		client.Send(wire.Envelope{T: "signal", ID: wire.GenID(), Ts: wire.Now(), Dev: dev, To: to,
			Enc: &wire.Enc{V: 1, Alg: "AES-256-GCM", IV: iv, Ct: ct}})
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	done := make(chan error, 1)
	var pickOnce sync.Once

	onMessage := func(env wire.Envelope) {
		switch env.T {
		case "signal":
			if env.Enc == nil {
				return
			}
			if pt, err := cipher.Open(env.Enc.IV, env.Enc.Ct); err == nil {
				mgr.OnSignal(env.Dev, pt)
			}
		case "roster":
			peers := rosterPeers(cipher, env.Devices, dev)
			if len(peers) == 0 {
				return
			}
			pickOnce.Do(func() {
				target := ""
				switch {
				case want != "":
					for _, p := range peers {
						if p.Dev == want || strings.EqualFold(p.Name, want) {
							target = p.Dev
						}
					}
				case len(peers) == 1:
					target = peers[0].Dev
				default:
					if d, ok := pickDevice(peers); ok {
						target = d
					}
				}
				if target == "" {
					done <- fmt.Errorf("device %q not found", want)
					return
				}
				go func() {
					for _, f := range files {
						if err := mgr.SendFile(target, f); err != nil {
							done <- err
							return
						}
					}
					done <- nil
				}()
			})
		}
	}

	label := filepath.Base(files[0])
	if len(files) > 1 {
		label = fmt.Sprintf("%d files", len(files))
	}
	go client.Run(ctx, onMessage)
	logger.Printf("connecting to %s to send %s…", relay, label)

	select {
	case err := <-done:
		if err != nil {
			notify("LinuxDrop", "Failed to send "+label)
			logger.Fatalf("send failed: %v", err)
		}
		notify("LinuxDrop", "Sent "+label)
		logger.Printf("sent %s ✓", label)
	case <-ctx.Done():
		notify("LinuxDrop", "Send timed out — is the other device online?")
		logger.Fatal("timed out — is the other device online and on the same network/key?")
	}
}

// rosterPeers decrypts roster entries into peers (excluding self), deduped by dev.
func rosterPeers(cipher *crypto.Cipher, devices []wire.RosterDevice, self string) []engine.RosterPeer {
	seen := map[string]bool{}
	var peers []engine.RosterPeer
	for _, d := range devices {
		if d.Dev == self || seen[d.Dev] {
			continue
		}
		seen[d.Dev] = true
		name, platform := d.Dev, ""
		if d.Enc != nil {
			if pt, err := cipher.Open(d.Enc.IV, d.Enc.Ct); err == nil {
				var p struct {
					Name     string `json:"name"`
					Platform string `json:"platform"`
				}
				if json.Unmarshal(pt, &p) == nil && p.Name != "" {
					name = p.Name
					platform = p.Platform
				}
			}
		}
		peers = append(peers, engine.RosterPeer{Dev: d.Dev, Name: name, Platform: platform})
	}
	return peers
}

// pickerTool picks a native dialog tool: kdialog on KDE, else zenity.
func pickerTool() string {
	if strings.Contains(strings.ToUpper(os.Getenv("XDG_CURRENT_DESKTOP")), "KDE") {
		if _, err := exec.LookPath("kdialog"); err == nil {
			return "kdialog"
		}
	}
	if _, err := exec.LookPath("zenity"); err == nil {
		return "zenity"
	}
	if _, err := exec.LookPath("kdialog"); err == nil {
		return "kdialog"
	}
	return ""
}

// pickFile shows a native file chooser; returns (path, ok).
func pickFile() (string, bool) {
	switch pickerTool() {
	case "kdialog":
		out, err := exec.Command("kdialog", "--getopenfilename", homeDir()).Output()
		p := strings.TrimSpace(string(out))
		return p, err == nil && p != ""
	case "zenity":
		out, err := exec.Command("zenity", "--file-selection", "--title=Send a file").Output()
		p := strings.TrimSpace(string(out))
		return p, err == nil && p != ""
	}
	return "", false
}

// pickDevice returns a peer dev id: the only peer, or a chooser when there are several.
func pickDevice(peers []engine.RosterPeer) (string, bool) {
	switch len(peers) {
	case 0:
		notify("LinuxDrop", "No connected device to send to")
		return "", false
	case 1:
		return peers[0].Dev, true
	}
	label := func(p engine.RosterPeer) string {
		if p.Platform != "" {
			return p.Name + " · " + p.Platform
		}
		return p.Name
	}
	switch pickerTool() {
	case "kdialog":
		args := []string{"--menu", "Send to which device?"}
		for _, p := range peers {
			args = append(args, p.Dev, label(p))
		}
		out, err := exec.Command("kdialog", args...).Output()
		d := strings.TrimSpace(string(out))
		return d, err == nil && d != ""
	case "zenity":
		args := []string{"--list", "--title=Send to", "--text=Choose a device", "--column=id", "--column=Device", "--hide-column=1", "--print-column=1"}
		for _, p := range peers {
			args = append(args, p.Dev, label(p))
		}
		out, err := exec.Command("zenity", args...).Output()
		d := strings.TrimSpace(string(out))
		return d, err == nil && d != ""
	}
	return peers[0].Dev, true
}

func homeDir() string { h, _ := os.UserHomeDir(); return h }

func notify(title, body string) { _ = exec.Command("notify-send", "-a", "LinuxDrop", title, body).Start() }

// downloadsDir returns (creating it) the user's Downloads dir (XDG, ~/Downloads fallback).
func downloadsDir() string {
	dir := ""
	if out, err := exec.Command("xdg-user-dir", "DOWNLOAD").Output(); err == nil {
		dir = strings.TrimSpace(string(out))
	}
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, "Downloads")
	}
	_ = os.MkdirAll(dir, 0o755)
	return dir
}
