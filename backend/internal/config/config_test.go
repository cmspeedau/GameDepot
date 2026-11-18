package config

import (
	"strings"
	"testing"
)

// baseEnv pins the required variables and blanks the store ones: config treats an
// empty value as unset, so no process-level unset is needed.
func baseEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://u:p@localhost/db")
	t.Setenv("EXTERNAL_URL", "http://localhost:4000")
	t.Setenv("GAMEDEPOT_STORE", "")
	t.Setenv("GAMEDEPOT_CREDENTIAL_KEY", "")
	t.Setenv("GAMEDEPOT_INTEGRITY_INTERVAL", "")
	t.Setenv("GAMEDEPOT_CLEANUP_INTERVAL", "")
}

func TestStoreEnabledRequiresKey(t *testing.T) {
	baseEnv(t)
	t.Setenv("GAMEDEPOT_STORE", "true")
	if _, err := Load(); err == nil {
		t.Fatal("enabling the store without a credential key must refuse to boot")
	} else if !strings.Contains(err.Error(), "GAMEDEPOT_CREDENTIAL_KEY") {
		t.Fatalf("err = %v, want it to name the missing variable", err)
	}
}

func TestMalformedKeyRefusedAtBoot(t *testing.T) {
	baseEnv(t)
	for _, key := range []string{"tooshort", strings.Repeat("z", 64), strings.Repeat("ab", 16)} {
		t.Setenv("GAMEDEPOT_CREDENTIAL_KEY", key)
		if _, err := Load(); err == nil {
			t.Errorf("Load() accepted a malformed key %q", key)
		}
	}
}

func TestValidKeysAccepted(t *testing.T) {
	baseEnv(t)
	t.Setenv("GAMEDEPOT_STORE", "true")
	t.Setenv("GAMEDEPOT_CREDENTIAL_KEY", strings.Repeat("ab", 32))
	cfg, err := Load()
	if err != nil {
		t.Fatalf("hex key rejected: %v", err)
	}
	if !cfg.StoreEnabled || cfg.CredentialKey == "" {
		t.Fatalf("cfg = %+v", cfg)
	}

	// The same size as base64, which is what `openssl rand -base64 32` prints.
	t.Setenv("GAMEDEPOT_CREDENTIAL_KEY", "oaXdA9W0XsU3DdF1WlRUlVCo0dLZbXp0CdqH9dGd4Xk=")
	if _, err := Load(); err != nil {
		t.Fatalf("base64 key rejected: %v", err)
	}
}

func TestStoreDisabledNeedsNoKey(t *testing.T) {
	baseEnv(t)
	if _, err := Load(); err != nil {
		t.Fatalf("the default configuration must boot without store settings: %v", err)
	}
}

func TestMaintenanceIntervalDefaultsAndOverride(t *testing.T) {
	baseEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IntegrityInterval.Hours() != 24 || cfg.CleanupInterval.Minutes() != 60 {
		t.Fatalf("defaults = %v / %v", cfg.IntegrityInterval, cfg.CleanupInterval)
	}
	t.Setenv("GAMEDEPOT_INTEGRITY_INTERVAL", "0")
	t.Setenv("GAMEDEPOT_CLEANUP_INTERVAL", "90s")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IntegrityInterval != 0 || cfg.CleanupInterval.String() != "1m30s" {
		t.Fatalf("override = %v / %v", cfg.IntegrityInterval, cfg.CleanupInterval)
	}
	t.Setenv("GAMEDEPOT_CLEANUP_INTERVAL", "not-a-duration")
	if _, err := Load(); err == nil {
		t.Fatal("an unparseable interval must fail at boot")
	}
}
