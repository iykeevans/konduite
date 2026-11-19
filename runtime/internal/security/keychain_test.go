package security_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/iykeevans/konduite/runtime/internal/security"
)

func TestKeychainStoresReadsAndDeletesACredential(t *testing.T) {
	keyring.MockInit()
	kc := security.OSKeychain{}
	const account = "https://api.example.com"

	if _, err := kc.Get(account); !errors.Is(err, security.ErrNotFound) {
		t.Fatalf("Get before Set: err = %v, want ErrNotFound", err)
	}
	if err := kc.Set(account, "krt_secret"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := kc.Get(account)
	if err != nil || got != "krt_secret" {
		t.Fatalf("Get = %q, %v, want the stored credential", got, err)
	}
	if _, err := kc.Get("https://other.example.com"); !errors.Is(err, security.ErrNotFound) {
		t.Errorf("Get for another server: err = %v, want ErrNotFound", err)
	}
	if err := kc.Delete(account); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := kc.Get(account); !errors.Is(err, security.ErrNotFound) {
		t.Errorf("Get after Delete: err = %v, want ErrNotFound", err)
	}
	if err := kc.Delete(account); err != nil {
		t.Errorf("Delete when nothing is stored: %v, want nil", err)
	}
}

func TestKeychainWithoutABackendFailsClearly(t *testing.T) {
	keyring.MockInitWithError(errors.New("the name org.freedesktop.secrets was not provided by any .service files"))
	t.Cleanup(keyring.MockInit)
	kc := security.OSKeychain{}

	for name, op := range map[string]func() error{
		"get":    func() error { _, err := kc.Get("a"); return err },
		"set":    func() error { return kc.Set("a", "krt_secret") },
		"delete": func() error { return kc.Delete("a") },
	} {
		t.Run(name, func(t *testing.T) {
			err := op()
			if !errors.Is(err, security.ErrUnavailable) {
				t.Fatalf("err = %v, want ErrUnavailable", err)
			}
			if msg := err.Error(); !strings.Contains(msg, "keychain") || strings.Contains(msg, "krt_secret") {
				t.Errorf("error %q should name the keychain and never echo the secret", msg)
			}
		})
	}
}
