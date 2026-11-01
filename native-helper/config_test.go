package main

import (
	"encoding/base64"
	"strings"
	"testing"
)

func sampleConfig() string {
	key := base64.StdEncoding.EncodeToString([]byte("12345678901234567890123456789012"))
	return "[Interface]\nPrivateKey = " + key + "\nAddress = 10.7.0.2/32\nDNS = 1.1.1.1\n[Peer]\nPublicKey = " + key + "\nEndpoint = vpn.example:51820\nAllowedIPs = 0.0.0.0/0, ::/0\nPersistentKeepalive = 25\n"
}
func TestParseConfig(t *testing.T) {
	c, err := parseConfig(sampleConfig())
	if err != nil {
		t.Fatal(err)
	}
	if c.Protocol != "wireguard" || len(c.Addresses) != 1 || len(c.Peers) != 1 {
		t.Fatalf("unexpected parse metadata: %#v", c)
	}
	raw := strings.Replace(sampleConfig(), "Address =", "Jc = 4\nJmin = 40\nJmax = 70\nS1 = 21\nS2 = 35\nH1 = 111\nH2 = 222\nH3 = 333\nH4 = 444\nAddress =", 1)
	c, err = parseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if c.Protocol != "amneziawg" {
		t.Fatal("AWG config not detected")
	}
}
func TestRejectUnsafeAndUnsupportedConfig(t *testing.T) {
	tests := map[string]string{
		"shell hook":      strings.Replace(sampleConfig(), "Address =", "PostUp = powershell.exe bad\nAddress =", 1),
		"unknown setting": strings.Replace(sampleConfig(), "Address =", "UnknownKey = secret\nAddress =", 1),
		"duplicate key":   strings.Replace(sampleConfig(), "Address =", "MTU = 1420\nMTU = 1400\nAddress =", 1),
		"missing peer":    strings.Split(sampleConfig(), "[Peer]")[0],
		"broken key":      strings.Replace(sampleConfig(), "PrivateKey = ", "PrivateKey = broken", 1),
		"unrouted DNS":    strings.Replace(sampleConfig(), "0.0.0.0/0, ::/0", "10.1.0.0/16", 1),
		"hostname DNS":    strings.Replace(sampleConfig(), "DNS = 1.1.1.1", "DNS = dns.example", 1),
		"zero port":       strings.Replace(sampleConfig(), ":51820", ":0", 1),
		"oversized":       strings.Repeat("x", maxConfigSize+1),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := parseConfig(raw); err == nil {
				t.Fatal("unsafe/unsupported config accepted")
			}
		})
	}
}
func TestErrorsDoNotEchoSecrets(t *testing.T) {
	raw := strings.Replace(sampleConfig(), "PrivateKey = ", "PrivateKey = DO_NOT_LEAK_THIS_", 1)
	_, err := parseConfig(raw)
	if err == nil || strings.Contains(err.Error(), "DO_NOT_LEAK_THIS") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRejectAllocationDrivingAWGSettings(t *testing.T) {
	for _, settings := range []string{
		"Jc=1\nJmin=80\nJmax=40", "Jc=4294967295", "Jmax=4294967295", "S1=4294967295", "ContentPaddingAddition=0-4294967295", "I1=<r 4294967295>", "I1=<r -1>", "I1=<r 8000><r 8000>", "I1=<not-supported secret>",
	} {
		raw := strings.Replace(sampleConfig(), "Address =", settings+"\nAddress =", 1)
		if _, err := parseConfig(raw); err == nil {
			t.Fatalf("accepted allocation-driving AWG field %q", settings)
		}
	}
	raw := strings.Replace(sampleConfig(), "Address =", "I1=<b 0x1234><r 20><t>\nAddress =", 1)
	if _, err := parseConfig(raw); err != nil {
		t.Fatalf("valid bounded custom packet rejected: %v", err)
	}
}
