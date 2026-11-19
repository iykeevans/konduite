// Package security keeps the runtime's credentials in the OS keychain.
package security

import (
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"
)

const service = "konduite-agenthub"

// Errors returned by Keychain implementations; match them with errors.Is.
var (
	ErrNotFound    = errors.New("no credential in the OS keychain")
	ErrUnavailable = errors.New("OS keychain unavailable")
)

// Keychain stores one secret per account. Get returns ErrNotFound when the
// account has none, and Delete of a missing account is not an error.
type Keychain interface {
	Get(account string) (string, error)
	Set(account, secret string) error
	Delete(account string) error
}

// OSKeychain is the macOS Keychain, Windows Credential Manager or a Linux
// Secret Service provider. There is deliberately no file fallback (ADR-008).
type OSKeychain struct{}

// Get returns the secret stored for account.
func (OSKeychain) Get(account string) (string, error) {
	secret, err := keyring.Get(service, account)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", unavailable(err)
	}
	return secret, nil
}

// Set stores secret for account, replacing any previous one.
func (OSKeychain) Set(account, secret string) error {
	if err := keyring.Set(service, account, secret); err != nil {
		return unavailable(err)
	}
	return nil
}

// Delete removes account's secret.
func (OSKeychain) Delete(account string) error {
	err := keyring.Delete(service, account)
	if err == nil || errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return unavailable(err)
}

func unavailable(err error) error {
	return fmt.Errorf("%w (%v): agenthub keeps credentials only in the OS keychain; "+
		"on Linux start a Secret Service provider such as gnome-keyring or KeePassXC and try again", ErrUnavailable, err)
}
