package tether

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	svcUUID    = "e3a9f5c0-1d2b-4e3a-9c8d-0a1b2c3d4e5f"
	nonceUUID  = "e3a9f5c1-1d2b-4e3a-9c8d-0a1b2c3d4e5f"
	cmdUUID    = "e3a9f5c2-1d2b-4e3a-9c8d-0a1b2c3d4e5f"
	statusUUID = "e3a9f5c3-1d2b-4e3a-9c8d-0a1b2c3d4e5f"
	bluez      = "org.bluez"
	adapter    = "/org/bluez/hci0"
)

// BLECentral talks to the phone's tether GATT service over BlueZ D-Bus.
type BLECentral struct{ key []byte }

func NewBLECentral(kBle []byte) *BLECentral { return &BLECentral{key: kBle} }

// Command connects to the phone (scanning if needed), sends one opcode, and returns the status
// result code. The phone resets its session nonce per connection, so we read a fresh nonce each call.
func (b *BLECentral) Command(opcode byte, seq uint32) (result byte, err error) {
	conn, err := dbus.SystemBus()
	if err != nil {
		return 0, err
	}
	devPath, err := b.findDevice(conn)
	if err != nil {
		return 0, err
	}
	dev := conn.Object(bluez, devPath)
	if err := b.connectResolved(conn, dev, devPath); err != nil {
		return 0, err
	}
	defer dev.Call("org.bluez.Device1.Disconnect", 0)

	nonceCh, err := b.charPath(conn, devPath, nonceUUID)
	if err != nil {
		return 0, err
	}
	cmdCh, err := b.charPath(conn, devPath, cmdUUID)
	if err != nil {
		return 0, err
	}
	nonce, err := b.readValue(conn, nonceCh)
	if err != nil {
		return 0, err
	}
	frame, err := SealCommand(b.key, nonce, seq, opcode)
	if err != nil {
		return 0, err
	}
	statusCh, _ := b.charPath(conn, devPath, statusUUID)
	resCh := make(chan byte, 1)
	if statusCh != "" {
		b.watchStatus(conn, statusCh, opcode, resCh)
	}
	if err := b.writeValue(conn, cmdCh, frame); err != nil {
		return 0, err
	}
	select {
	case r := <-resCh:
		return r, nil
	case <-time.After(8 * time.Second):
		return 0, nil // command delivered; phone didn't notify a status in time
	}
}

// findDevice scans (filtered to our service UUID) and returns the BlueZ device object path.
func (b *BLECentral) findDevice(conn *dbus.Conn) (dbus.ObjectPath, error) {
	ad := conn.Object(bluez, adapter)
	ad.Call("org.bluez.Adapter1.SetDiscoveryFilter", 0, map[string]dbus.Variant{
		"UUIDs":     dbus.MakeVariant([]string{svcUUID}),
		"Transport": dbus.MakeVariant("le"),
	})
	ad.Call("org.bluez.Adapter1.StartDiscovery", 0)
	defer ad.Call("org.bluez.Adapter1.StopDiscovery", 0)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if objs, err := managedObjects(conn); err == nil {
			for path, ifaces := range objs {
				d, ok := ifaces["org.bluez.Device1"]
				if ok && uuidsContain(d, svcUUID) {
					return path, nil
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return "", errors.New("tether: phone not found over BLE")
}

// connectResolved connects and waits for GATT to resolve, retrying (BlueZ can drop mid-discovery).
func (b *BLECentral) connectResolved(conn *dbus.Conn, dev dbus.BusObject, devPath dbus.ObjectPath) error {
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if call := dev.Call("org.bluez.Device1.Connect", 0); call.Err != nil {
			lastErr = call.Err
			time.Sleep(time.Second)
			continue
		}
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			if v, err := getProp(conn, devPath, "org.bluez.Device1", "ServicesResolved"); err == nil {
				if resolved, _ := v.Value().(bool); resolved {
					return nil
				}
			}
			if c, err := getProp(conn, devPath, "org.bluez.Device1", "Connected"); err == nil {
				if connected, _ := c.Value().(bool); !connected {
					break // dropped during discovery; retry
				}
			}
			time.Sleep(300 * time.Millisecond)
		}
		lastErr = errors.New("services did not resolve")
		dev.Call("org.bluez.Device1.Disconnect", 0)
		time.Sleep(time.Second)
	}
	return fmt.Errorf("tether: connect failed: %w", lastErr)
}

func (b *BLECentral) charPath(conn *dbus.Conn, devPath dbus.ObjectPath, uuid string) (dbus.ObjectPath, error) {
	objs, err := managedObjects(conn)
	if err != nil {
		return "", err
	}
	for path, ifaces := range objs {
		if !strings.HasPrefix(string(path), string(devPath)) {
			continue
		}
		ch, ok := ifaces["org.bluez.GattCharacteristic1"]
		if !ok {
			continue
		}
		if u, _ := ch["UUID"].Value().(string); strings.EqualFold(u, uuid) {
			return path, nil
		}
	}
	return "", fmt.Errorf("tether: characteristic %s not found", uuid)
}

func (b *BLECentral) readValue(conn *dbus.Conn, ch dbus.ObjectPath) ([]byte, error) {
	var out []byte
	err := conn.Object(bluez, ch).Call("org.bluez.GattCharacteristic1.ReadValue", 0,
		map[string]dbus.Variant{}).Store(&out)
	return out, err
}

func (b *BLECentral) writeValue(conn *dbus.Conn, ch dbus.ObjectPath, val []byte) error {
	return conn.Object(bluez, ch).Call("org.bluez.GattCharacteristic1.WriteValue", 0,
		val, map[string]dbus.Variant{}).Err
}

func (b *BLECentral) watchStatus(conn *dbus.Conn, ch dbus.ObjectPath, opcode byte, out chan<- byte) {
	conn.Object(bluez, ch).Call("org.bluez.GattCharacteristic1.StartNotify", 0)
	sig := make(chan *dbus.Signal, 8)
	conn.Signal(sig)
	conn.AddMatchSignal(dbus.WithMatchObjectPath(ch),
		dbus.WithMatchInterface("org.freedesktop.DBus.Properties"),
		dbus.WithMatchMember("PropertiesChanged"))
	go func() {
		for s := range sig {
			if s.Path != ch || len(s.Body) < 2 {
				continue
			}
			changed, _ := s.Body[1].(map[string]dbus.Variant)
			v, ok := changed["Value"]
			if !ok {
				continue
			}
			raw, _ := v.Value().([]byte)
			if op, res, err := OpenStatus(b.key, raw); err == nil && op == opcode {
				select {
				case out <- res:
				default:
				}
				return
			}
		}
	}()
}

// --- small D-Bus helpers ---

func managedObjects(conn *dbus.Conn) (map[dbus.ObjectPath]map[string]map[string]dbus.Variant, error) {
	var objs map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	err := conn.Object(bluez, "/").Call("org.freedesktop.DBus.ObjectManager.GetManagedObjects", 0).Store(&objs)
	return objs, err
}

func getProp(conn *dbus.Conn, path dbus.ObjectPath, iface, name string) (dbus.Variant, error) {
	return conn.Object(bluez, path).GetProperty(iface + "." + name)
}

func uuidsContain(dev map[string]dbus.Variant, uuid string) bool {
	v, ok := dev["UUIDs"]
	if !ok {
		return false
	}
	list, _ := v.Value().([]string)
	for _, u := range list {
		if strings.EqualFold(u, uuid) {
			return true
		}
	}
	return false
}
