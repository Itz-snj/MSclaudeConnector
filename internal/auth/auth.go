package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/Itz-snj/MSclaudeConnector/internal/store"
)

// TokenTTL is how long a one-time pairing token remains valid.
const TokenTTL = 5 * time.Minute

// Auth manages pairing tokens and device credentials.
type Auth struct {
	store *store.Store
}

func New(st *store.Store) *Auth {
	return &Auth{store: st}
}

// GeneratePairingToken creates a new one-time pairing token.
func (a *Auth) GeneratePairingToken() (string, error) {
	raw, err := randomBytes(32)
	if err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	if err := a.store.CreatePendingToken(token, time.Now().Add(TokenTTL)); err != nil {
		return "", err
	}
	return token, nil
}

// ApprovePairingToken marks a pending token as approved with a human-readable
// device name. This is the "physical confirmation on PC A" step.
func (a *Auth) ApprovePairingToken(token, name string) error {
	pt, err := a.store.GetPendingToken(token)
	if err != nil {
		return err
	}
	if pt == nil {
		return errors.New("token not found")
	}
	if time.Now().After(pt.ExpiresAt) {
		return errors.New("token expired")
	}
	return a.store.ApprovePendingToken(token, name)
}

// ConsumePairingToken validates a token, ensures it was approved, and deletes
// it so it cannot be reused.
func (a *Auth) ConsumePairingToken(token string) (name string, ok bool) {
	pt, err := a.store.ConsumePendingToken(token)
	if err != nil || pt == nil {
		return "", false
	}
	return pt.Name, true
}

// IssueCredential creates a new device credential and stores its hash.
func (a *Auth) IssueCredential(deviceID, name string) (string, error) {
	raw, err := randomBytes(32)
	if err != nil {
		return "", err
	}
	credential := hex.EncodeToString(raw)
	if err := a.store.AddDevice(deviceID, name, credential); err != nil {
		return "", err
	}
	return credential, nil
}

// ValidateCredential checks a bearer credential and returns the device ID.
func (a *Auth) ValidateCredential(credential string) (deviceID string, ok bool) {
	d, err := a.store.GetDeviceByCredential(credential)
	if err != nil || d == nil || d.Revoked {
		return "", false
	}
	_ = a.store.UpdateDeviceLastSeen(d.DeviceID)
	return d.DeviceID, true
}

// RevokeDevice revokes a device's credential.
func (a *Auth) RevokeDevice(deviceID string) error {
	return a.store.RevokeDevice(deviceID)
}

func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	_, err := rand.Read(b)
	return b, err
}

// HashForDisplay returns a short, non-secret display hash of a credential.
func HashForDisplay(credential string) string {
	h := sha256.Sum256([]byte(credential))
	return hex.EncodeToString(h[:8])
}
