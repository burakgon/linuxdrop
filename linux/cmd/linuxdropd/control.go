package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// controlSockPath is the unix socket the running daemon listens on so the CLI (`linuxdropd tether
// on|off|status`) can drive it — keeping the tray in sync and letting the daemon hold the keepalive,
// instead of the CLI acting as a disconnected one-shot.
func controlSockPath() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "linuxdrop.sock")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("linuxdrop-%d.sock", os.Getuid()))
}

// serveControl accepts one-line commands (on/off/status) and routes them to the live orchestrator.
func serveControl(ctx context.Context, logger *log.Logger, toggle func(connect bool) error, status func() string) {
	path := controlSockPath()
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		logger.Printf("control socket: %v (CLI tether control disabled)", err)
		return
	}
	go func() { <-ctx.Done(); _ = ln.Close(); _ = os.Remove(path) }()
	for {
		conn, err := ln.Accept()
		if err != nil {
			return // listener closed on shutdown
		}
		go serveControlConn(conn, toggle, status)
	}
}

func serveControlConn(conn net.Conn, toggle func(connect bool) error, status func() string) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(120 * time.Second)) // a BLE connect can take ~25s
	sc := bufio.NewScanner(conn)
	if !sc.Scan() {
		return
	}
	switch strings.TrimSpace(sc.Text()) {
	case "on":
		if err := toggle(true); err != nil {
			fmt.Fprintf(conn, "err: %v\n", err)
		} else {
			fmt.Fprintln(conn, "tether: up")
		}
	case "off":
		_ = toggle(false)
		fmt.Fprintln(conn, "tether: off")
	case "status":
		fmt.Fprintln(conn, status())
	default:
		fmt.Fprintln(conn, "err: unknown command")
	}
}

// sendControl forwards a command to the running daemon; ok=false means no daemon is listening.
func sendControl(cmd string) (reply string, ok bool) {
	conn, err := net.DialTimeout("unix", controlSockPath(), 500*time.Millisecond)
	if err != nil {
		return "", false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(125 * time.Second))
	fmt.Fprintln(conn, cmd)
	b, _ := io.ReadAll(conn)
	return string(b), true
}

// tetherFailMsg turns an orchestrator error into a human, actionable notification line.
func tetherFailMsg(err error) string {
	s := err.Error()
	switch {
	case strings.Contains(s, "still offline"):
		return "Joined the hotspot but there's no internet — is the phone's mobile data on?"
	case strings.Contains(s, "could not start"), strings.Contains(s, "error code"):
		return "The phone couldn't start its hotspot."
	case strings.Contains(s, "not found"), strings.Contains(s, "did not resolve"),
		strings.Contains(s, "timed out"), strings.Contains(s, "unreachable"),
		strings.Contains(s, "connect failed"):
		return "Couldn't reach your phone — is its Bluetooth on and the LinuxDrop app running?"
	default:
		return "Couldn't connect to the phone over Bluetooth."
	}
}
