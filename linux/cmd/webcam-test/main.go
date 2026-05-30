package main
import (
	"context"; "encoding/json"; "log"; "os"; "strings"; "time"
	"linuxdrop/linux/internal/blob"; "linuxdrop/linux/internal/clipboard"
	"linuxdrop/linux/internal/config"; "linuxdrop/linux/internal/crypto"
	"linuxdrop/linux/internal/engine"; "linuxdrop/linux/internal/webcam"
	"linuxdrop/linux/internal/wire"; "linuxdrop/linux/internal/ws"
)
func main() {
	logger := log.New(os.Stderr, "wctest ", log.LstdFlags|log.Lmsgprefix)
	cfg, _ := config.Load()
	hexS, _ := config.LoadSecret()
	secret, _ := config.DecodeSecretHex(hexS)
	cipher, _ := crypto.NewCipher(secret)
	roomID := crypto.RoomID(secret)
	helloPT, _ := json.Marshal(map[string]string{"name": cfg.DeviceName, "platform": "linux"})
	iv, ct, _ := cipher.Seal(helloPT)
	hello := &wire.Enc{V: 1, Alg: "AES-256-GCM", IV: iv, Ct: ct}
	client := ws.NewClient(cfg.RelayURL, roomID, cfg.DeviceID, hello, logger, func(b bool){})
	eng := engine.New(cipher, clipboard.NewWayland(), client, cfg.DeviceID, roomID, blob.NewClient(cfg.RelayURL), logger)
	mgr := webcam.NewManager(webcam.Config{
		Device: "/dev/video20",
		ResolveTarget: func(name string) string {
			for _, p := range eng.Peers() { if p.Platform == "android" && strings.Contains(p.Name, "OPPO") { return p.Dev } }
			return name
		},
		Emit: func(toDev string, payload []byte) error { eng.SendSignal(toDev, payload); return nil },
		Logger: logger,
	})
	eng.SetOnSignal(func(fromDev string, payload []byte) {
		var head struct{ Kind string `json:"kind"` }
		_ = json.Unmarshal(payload, &head)
		if strings.HasPrefix(head.Kind, "webcam-") { mgr.OnSignal(fromDev, payload) }
	})
	ctx, cancel := context.WithCancel(context.Background())
	go eng.Run(ctx)
	time.Sleep(3 * time.Second)
	var target string
	for _, p := range eng.Peers() { if p.Platform == "android" && strings.Contains(p.Name, "OPPO") { target = p.Dev } }
	if target == "" { logger.Fatal("no OPPO peer"); cancel(); return }
	go mgr.Start(ctx, webcam.StartOpts{Target: target, Width: 1280, Height: 720, FPS: 30, Camera: "back", Codec: "h264"})
	dur, _ := time.ParseDuration(os.Getenv("WCTEST_DURATION") + "s")
	if dur == 0 { dur = 120 * time.Second }
	time.Sleep(dur)
	mgr.Stop("test-done"); cancel()
	time.Sleep(2 * time.Second)
}
