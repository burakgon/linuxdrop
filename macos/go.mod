module linuxdrop/macos

go 1.26.3

require (
	github.com/mdp/qrterminal/v3 v3.2.1
	linuxdrop/shared v0.0.0
)

require (
	fyne.io/systray v1.12.2 // indirect
	github.com/coder/websocket v1.8.15 // indirect
	github.com/danieljoos/wincred v1.2.3 // indirect
	github.com/godbus/dbus/v5 v5.2.2 // indirect
	github.com/zalando/go-keyring v0.2.8 // indirect
	golang.org/x/sys v0.29.0 // indirect
	golang.org/x/term v0.13.0 // indirect
	rsc.io/qr v0.2.0 // indirect
)

replace linuxdrop/shared => ../shared
