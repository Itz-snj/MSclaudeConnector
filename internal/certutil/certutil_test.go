package certutil

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrGenerateSANsAndStableSPKI(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")

	want := SANs{
		DNSNames: []string{"localhost", "host.local"},
		IPs:      []net.IP{net.IPv4(127, 0, 0, 1), net.ParseIP("192.168.1.50")},
	}

	cert, pins, regen, err := LoadOrGenerate(certPath, keyPath, want)
	if err != nil {
		t.Fatalf("load/generate: %v", err)
	}
	if !regen {
		t.Fatal("expected first call to generate")
	}
	if len(cert.Certificate) == 0 {
		t.Fatal("expected certificate bytes")
	}
	if pins.SPKI == "" || pins.Certificate == "" {
		t.Fatalf("expected pins, got %+v", pins)
	}

	leaf, err := ParsePEMCert(certPath)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	if !covers(leaf, want) {
		t.Fatalf("generated cert does not cover wanted SANs: dns=%v ips=%v", leaf.DNSNames, leaf.IPAddresses)
	}
	if FingerprintFromCert(leaf) != pins.Certificate {
		t.Fatal("certificate pin does not match on-disk cert")
	}

	// Second call with identical SANs must not regenerate or change pins.
	_, pins2, regen2, err := LoadOrGenerate(certPath, keyPath, want)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if regen2 {
		t.Fatal("did not expect regeneration for unchanged SANs")
	}
	if pins2 != pins {
		t.Fatalf("pins changed unexpectedly: %+v -> %+v", pins, pins2)
	}

	// Adding a SAN regenerates the certificate but must keep the SPKI stable.
	want2 := SANs{
		DNSNames: append([]string{}, want.DNSNames...),
		IPs:      append([]net.IP{}, want.IPs...),
	}
	want2.IPs = append(want2.IPs, net.ParseIP("10.0.0.5"))

	_, pins3, regen3, err := LoadOrGenerate(certPath, keyPath, want2)
	if err != nil {
		t.Fatalf("regen for new SAN: %v", err)
	}
	if !regen3 {
		t.Fatal("expected regeneration when a SAN is uncovered")
	}
	if pins3.SPKI != pins.SPKI {
		t.Fatal("SPKI must be stable across certificate regeneration")
	}
	if pins3.Certificate == pins.Certificate {
		t.Fatal("certificate fingerprint should change when regenerated")
	}
	leaf2, err := ParsePEMCert(certPath)
	if err != nil {
		t.Fatalf("parse regenerated cert: %v", err)
	}
	if !covers(leaf2, want2) {
		t.Fatalf("regenerated cert does not cover new SANs: dns=%v ips=%v", leaf2.DNSNames, leaf2.IPAddresses)
	}
}

func TestKeyLossChangesSPKI(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	want := SANs{DNSNames: []string{"localhost"}, IPs: []net.IP{net.IPv4(127, 0, 0, 1)}}

	_, pins1, _, err := LoadOrGenerate(certPath, keyPath, want)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := os.Remove(keyPath); err != nil {
		t.Fatalf("remove key: %v", err)
	}
	_, pins2, regen, err := LoadOrGenerate(certPath, keyPath, want)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if !regen {
		t.Fatal("expected regeneration when key is missing")
	}
	if pins2.SPKI == pins1.SPKI {
		t.Fatal("expected SPKI to change when the key is lost")
	}
}
