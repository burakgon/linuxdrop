// Command bgnconnectd is the Linux clipboard-sync daemon.
//
//	bgnconnectd gen-secret                 print a new random secret (hex)
//	bgnconnectd pair <uri|hex> [relay]     store secret (+relay) for this device
//	bgnconnectd qr                         print the pairing URI + QR (terminal)
//	bgnconnectd send <file> [device]       send a file directly (P2P) to a peer
//	bgnconnectd [run] [--relay U] [--dev-secret HEX] [--no-tray]
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

	"bgnconnect/linux/internal/blob"
	"bgnconnect/linux/internal/clipboard"
	"bgnconnect/linux/internal/config"
	"bgnconnect/linux/internal/crypto"
	"bgnconnect/linux/internal/engine"
	"bgnconnect/linux/internal/p2p"
	"bgnconnect/linux/internal/tray"
	"bgnconnect/linux/internal/wire"
	"bgnconnect/linux/internal/ws"
)

func main() {
	logger := log.New(os.Stderr, "bgnconnect ", log.LstdFlags|log.Lmsgprefix)

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
		logger.Fatal("usage: bgnconnectd pair <bgnconnect://... | hex> [relay-url]")
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
		logger.Fatal("no relay URL — pair with a bgnconnect:// URI that includes one, or: bgnconnectd pair <hex> <wss://your-relay>")
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
		logger.Fatal("no relay configured — set one first: bgnconnectd pair <hex> <wss://your-relay>")
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
			logger.Fatal("not paired; run: bgnconnectd pair <uri|hex> [relay]  (or pass --dev-secret)")
		}
		if secret, err = config.DecodeSecretHex(hexS); err != nil {
			logger.Fatalf("stored secret invalid: %v", err)
		}
	}
	if relay == "" {
		logger.Fatal("no relay URL; pair with a bgnconnect:// URI or pass --relay")
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
		OnShowQR: func() { showPairingQR(logger, secret, relay) },
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
	path := filepath.Join(os.TempDir(), "bgnconnect-pair.png")
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
	_ = fs.Parse(args)
	rest := fs.Args()
	if len(rest) < 1 {
		logger.Fatal("usage: bgnconnectd send [--relay U] [--dev-secret H] <file> [device]")
	}
	file := rest[0]
	if _, err := os.Stat(file); err != nil {
		logger.Fatalf("file: %v", err)
	}
	want := ""
	if len(rest) >= 2 {
		want = rest[1]
	}

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
			logger.Fatal("not paired; run: bgnconnectd pair <uri|hex> <relay>")
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
	client := ws.NewClient(relay, roomID, dev, buildHelloEnc(cipher, "bgnconnect (sender)"), logger, func(bool) {})
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
	var once sync.Once
	fire := func(target string) {
		once.Do(func() { go func() { done <- mgr.SendFile(target, file) }() })
	}

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
			if target := pickTarget(cipher, env.Devices, dev, want); target != "" {
				fire(target)
			}
		}
	}

	go client.Run(ctx, onMessage)
	logger.Printf("connecting to %s to send %q…", relay, filepath.Base(file))

	select {
	case err := <-done:
		if err != nil {
			logger.Fatalf("send failed: %v", err)
		}
		logger.Printf("sent %q ✓", filepath.Base(file))
	case <-ctx.Done():
		logger.Fatal("timed out — is the other device online and on the same network/key?")
	}
}

// pickTarget chooses a recipient dev id from the roster: matches the given name/id,
// or the sole other device when none is specified.
func pickTarget(cipher *crypto.Cipher, devices []wire.RosterDevice, self, want string) string {
	type cand struct{ dev, name string }
	var others []cand
	for _, d := range devices {
		if d.Dev == self {
			continue
		}
		name := d.Dev
		if d.Enc != nil {
			if pt, err := cipher.Open(d.Enc.IV, d.Enc.Ct); err == nil {
				var p struct {
					Name string `json:"name"`
				}
				if json.Unmarshal(pt, &p) == nil && p.Name != "" {
					name = p.Name
				}
			}
		}
		others = append(others, cand{d.Dev, name})
	}
	if len(others) == 0 {
		return ""
	}
	if want == "" {
		return others[0].dev
	}
	for _, c := range others {
		if c.dev == want || strings.EqualFold(c.name, want) {
			return c.dev
		}
	}
	return ""
}

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
