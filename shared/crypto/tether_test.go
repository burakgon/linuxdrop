package crypto

import (
	"encoding/hex"
	"testing"
)

func TestTetherDerivations(t *testing.T) {
	secret := mustHex(t, "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f")
	if got := hex.EncodeToString(TetherBLEKey(secret)); got != "793b6d391031856ed02410d54050c062f02ec2a696c4b3b615e22ff56f130f99" {
		t.Fatalf("K_ble = %s", got)
	}
	if got := TetherSSID(secret); got != "LD-2f0d61cb" {
		t.Fatalf("ssid = %s", got)
	}
	if got := TetherPSK(secret); got != "9ddc1c62b4f9a1da71d45bab" {
		t.Fatalf("psk = %s", got)
	}
}
