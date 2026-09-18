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
	"math/big"
	"net"
	"os"
	"time"
)

// LoadOrGenerate loads an existing TLS key pair or generates a new self-signed
// ECDSA certificate. It returns the loaded/generated certificate plus a
// SHA-256 fingerprint of the certificate (for QR pinning).
func LoadOrGenerate(certPath, keyPath string) (tls.Certificate, string, error) {
	if exists(certPath) && exists(keyPath) {
		cert, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return tls.Certificate{}, "", err
		}
		fp, err := fingerprintFromFile(certPath)
		if err != nil {
			return tls.Certificate{}, "", err
		}
		return cert, fp, nil
	}

	certPEM, keyPEM, fp, err := generateSelfSigned()
	if err != nil {
		return tls.Certificate{}, "", err
	}
	if err := os.WriteFile(certPath, certPEM, 0600); err != nil {
		return tls.Certificate{}, "", err
	}
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		return tls.Certificate{}, "", err
	}

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	return cert, fp, nil
}

func generateSelfSigned() (certPEM, keyPEM []byte, fingerprint string, err error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, "", err
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"harness-local"},
		},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}

	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return nil, nil, "", err
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	fp := sha256.Sum256(der)
	fingerprint = hex.EncodeToString(fp[:])

	keyBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, nil, "", err
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})

	return certPEM, keyPEM, fingerprint, nil
}

func fingerprintFromFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return "", errors.New("failed to decode certificate PEM")
	}
	fp := sha256.Sum256(block.Bytes)
	return hex.EncodeToString(fp[:]), nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
