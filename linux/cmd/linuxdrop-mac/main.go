//go:build darwin

// Command linuxdrop-mac is the macOS menu bar clipboard-sync client.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/mdp/qrterminal/v3"

	"linuxdrop/linux/internal/blob"
	"linuxdrop/linux/internal/clipboard"
	"linuxdrop/linux/internal/config"
	"linuxdrop/linux/internal/crypto"
	"linuxdrop/linux/internal/engine"
	"linuxdrop/linux/internal/tray"
	"linuxdrop/linux/internal/wire"
	"linuxdrop/linux/internal/ws"
)

func main() {
	logger := log.New(os.Stderr, "linuxdrop ", log.LstdFlags|log.Lmsgprefix)
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "pair" {
		pair(logger, args[1:])
		return
	}
	if len(args) > 0 && args[0] == "qr" {
		showQR(logger, args[1:])
		return
	}
	if len(args) > 0 && args[0] != "run" && !strings.HasPrefix(args[0], "-") {
		logger.Fatal("usage: linuxdrop-mac qr [relay-url] | pair <linuxdrop://...> | run [--relay URL]")
	}
	args = dropRun(args)
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	relayFlag := fs.String("relay", "", "override relay URL (ws:// or wss://)")
	_ = fs.Parse(args)

	cfg, err := config.Load()
	if err != nil {
		logger.Fatalf("config: %v", err)
	}
	relay := cfg.RelayURL
	if *relayFlag != "" {
		relay = *relayFlag
	}
	if relay == "" {
		logger.Fatal("not paired; run: linuxdrop-mac pair '<linuxdrop://pair?...>'")
	}
	secretHex, _ := config.LoadSecret()
	secret, err := config.DecodeSecretHex(secretHex)
	if err != nil {
		logger.Fatalf("no valid pairing secret; run: linuxdrop-mac pair '<linuxdrop://pair?...>': %v", err)
	}
	cipher, err := crypto.NewCipher(secret)
	if err != nil {
		logger.Fatalf("cipher: %v", err)
	}
	room := crypto.RoomID(secret)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var tr *tray.Tray
	hello, err := json.Marshal(map[string]string{"name": cfg.DeviceName, "platform": "macOS"})
	if err != nil {
		logger.Fatalf("hello: %v", err)
	}
	iv, ct, err := cipher.Seal(hello)
	if err != nil {
		logger.Fatalf("hello encrypt: %v", err)
	}
	client := ws.NewClient(relay, room, cfg.DeviceID,
		&wire.Enc{V: 1, Alg: "AES-256-GCM", IV: iv, Ct: ct}, logger,
		func(connected bool) {
			if tr != nil {
				tr.SetConnected(connected)
			}
		})
	eng := engine.New(cipher, clipboard.NewMacOS(), client, cfg.DeviceID, room, blob.NewClient(relay), logger)
	eng.SetOnRoster(func(devices []string) {
		if tr != nil {
			tr.SetDevices(devices)
		}
	})
	go func() {
		if err := eng.Run(ctx); err != nil && ctx.Err() == nil {
			logger.Printf("engine: %v", err)
			stop()
		}
	}()
	tr = tray.New(tray.Callbacks{
		OnQuit:        stop,
		OnTogglePause: eng.SetPaused,
	})
	go func() {
		<-ctx.Done()
		tr.Quit()
	}()
	logger.Printf("running: room=%s dev=%s relay=%s", room, cfg.DeviceID, relay)
	tr.Run()
}

func showQR(logger *log.Logger, args []string) {
	if len(args) > 1 {
		logger.Fatal("usage: linuxdrop-mac qr [relay-url]")
	}
	cfg, err := config.Load()
	if err != nil {
		logger.Fatalf("config: %v", err)
	}
	if len(args) == 1 {
		cfg.RelayURL = args[0]
	}
	if cfg.RelayURL == "" {
		logger.Fatal("no relay configured; use: linuxdrop-mac qr wss://relay.example.com")
	}
	secretHex, _ := config.LoadSecret()
	if secretHex == "" {
		secretHex = config.HexSecret(config.GenerateSecret())
		if err := config.SaveSecret(secretHex); err != nil {
			logger.Fatalf("save secret: %v", err)
		}
	}
	secret, err := config.DecodeSecretHex(secretHex)
	if err != nil {
		logger.Fatalf("stored secret invalid: %v", err)
	}
	if err := cfg.Save(); err != nil {
		logger.Fatalf("save config: %v", err)
	}
	uri := config.PairingURI(secret, cfg.RelayURL)
	fmt.Println("Scan this QR code with LinuxDrop on Android:")
	qrterminal.GenerateHalfBlock(uri, qrterminal.M, os.Stdout)
}

func dropRun(args []string) []string {
	if len(args) > 0 && args[0] == "run" {
		return args[1:]
	}
	return args
}

func pair(logger *log.Logger, args []string) {
	if len(args) != 1 {
		logger.Fatal("usage: linuxdrop-mac pair '<linuxdrop://pair?s=...&relay=...>'")
	}
	secret, relay, err := config.ParsePairing(args[0])
	if err != nil {
		logger.Fatalf("pair: %v", err)
	}
	if relay == "" {
		logger.Fatal("pairing URI must include a relay URL")
	}
	if err := config.SaveSecret(config.HexSecret(secret)); err != nil {
		logger.Fatalf("save secret: %v", err)
	}
	cfg, err := config.Load()
	if err != nil {
		logger.Fatalf("config: %v", err)
	}
	cfg.RelayURL = relay
	if err := cfg.Save(); err != nil {
		logger.Fatalf("save config: %v", err)
	}
	fmt.Printf("Paired with relay %s. Start the menu bar client with: linuxdrop-mac run\n", relay)
}
