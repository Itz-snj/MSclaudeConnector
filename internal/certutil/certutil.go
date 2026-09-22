package certutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// regenWindow is how close to expiry a certificate may get before it is
// regenerated on startup.
const regenWindow = 30 * 24 * time.Hour

// SANs is the desired subject-alternative-name set for the host certificate.
type SANs struct {
	DNSNames []string
	IPs      []net.IP
}

// Pins are the public values a client can use to pin the host identity. The
// SPKI is stable across certificate regeneration as long as key.pem is reused.
type Pins struct {
	SPKI        string // sha256 hex of the DER-encoded SubjectPublicKeyInfo
	Certificate string // sha256 hex of the DER-encoded certificate
}

// LoadOrGenerate loads an existing TLS key pair or generates a new self-signed
// ECDSA certificate covering want. It returns the certificate, its pins, and
// whether the certificate was (re)generated on this call.
//
// If key.pem exists it is reused, so regenerating the certificate does not
// change the SPKI and paired clients need not re-pair.
func LoadOrGenerate(certPath, keyPath string, want SANs) (tls.Certificate, Pins, bool, error) {
	certExists := exists(certPath)
	keyExists := exists(keyPath)

	if certExists && keyExists {
		cert, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err == nil {
			if leaf, perr := x509.ParseCertificate(cert.Certificate[0]); perr == nil {
				pins, perr := pinsFromLeaf(leaf)
				if perr == nil && covers(leaf, want) && !nearExpiry(leaf) {
					return cert, pins, false, nil
				}
			}
		}
		// Fall through to regeneration; the existing key (if any) is reused.
	}

	priv, err := loadOrGenerateKey(keyPath, keyExists)
	if err != nil {
		return tls.Certificate{}, Pins{}, false, err
	}

	certPEM, keyPEM, pins, err := generateSelfSigned(want, priv)
	if err != nil {
		return tls.Certificate{}, Pins{}, false, err
	}

	if err := writeFileAtomic(keyPath, keyPEM, 0600); err != nil {
		return tls.Certificate{}, Pins{}, false, err
	}
	if err := writeFileAtomic(certPath, certPEM, 0600); err != nil {
		return tls.Certificate{}, Pins{}, false, err
	}

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, Pins{}, false, err
	}
	return cert, pins, true, nil
}

// loadOrGenerateKey reuses key.pem when present and parseable, otherwise mints
// a fresh P-256 key.
func loadOrGenerateKey(keyPath string, exists bool) (*ecdsa.PrivateKey, error) {
	if exists {
		data, err := os.ReadFile(keyPath)
		if err == nil {
			if priv, err := parseECKey(data); err == nil {
				return priv, nil
			}
		}
	}
	return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
}

func parseECKey(pemData []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(pemData)
	if block == nil {
		return nil, errors.New("failed to decode key PEM")
	}
	if key, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("key is not an ECDSA private key")
	}
	return key, nil
}

func generateSelfSigned(want SANs, priv *ecdsa.PrivateKey) (certPEM, keyPEM []byte, pins Pins, err error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, Pins{}, err
	}

	template := x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"harness-local"},
		},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              dedupeStrings(want.DNSNames),
		IPAddresses:           dedupeIPs(want.IPs),
	}

	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return nil, nil, Pins{}, err
	}

	spkiDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		return nil, nil, Pins{}, err
	}
	spkiSum := sha256.Sum256(spkiDER)
	certSum := sha256.Sum256(der)
	pins = Pins{
		SPKI:        hex.EncodeToString(spkiSum[:]),
		Certificate: hex.EncodeToString(certSum[:]),
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	keyBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, nil, Pins{}, err
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})

	return certPEM, keyPEM, pins, nil
}

func pinsFromLeaf(leaf *x509.Certificate) (Pins, error) {
	spkiDER, err := x509.MarshalPKIXPublicKey(leaf.PublicKey)
	if err != nil {
		return Pins{}, err
	}
	spkiSum := sha256.Sum256(spkiDER)
	certSum := sha256.Sum256(leaf.Raw)
	return Pins{
		SPKI:        hex.EncodeToString(spkiSum[:]),
		Certificate: hex.EncodeToString(certSum[:]),
	}, nil
}

func covers(leaf *x509.Certificate, want SANs) bool {
	for _, dns := range want.DNSNames {
		found := false
		for _, have := range leaf.DNSNames {
			if have == dns {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	for _, ip := range want.IPs {
		found := false
		for _, have := range leaf.IPAddresses {
			if have.Equal(ip) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func nearExpiry(leaf *x509.Certificate) bool {
	return time.Now().Add(regenWindow).After(leaf.NotAfter)
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func dedupeStrings(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func dedupeIPs(in []net.IP) []net.IP {
	out := make([]net.IP, 0, len(in))
	for _, ip := range in {
		if ip == nil {
			continue
		}
		dup := false
		for _, have := range out {
			if have.Equal(ip) {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, ip)
		}
	}
	return out
}

// FingerprintFromCert returns the SHA-256 hex of a certificate's DER bytes.
func FingerprintFromCert(leaf *x509.Certificate) string {
	sum := sha256.Sum256(leaf.Raw)
	return hex.EncodeToString(sum[:])
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// ParsePEMCert parses the first certificate in a PEM file (used by tests).
func ParsePEMCert(path string) (*x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("failed to decode certificate PEM at %s", path)
	}
	return x509.ParseCertificate(block.Bytes)
}
