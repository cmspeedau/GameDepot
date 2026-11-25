package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/cmspeedau/gamedepot/internal/secrets"
)

type Config struct {
	DatabaseURL string
	Port        string
	ExternalURL string
	LibraryDir  string
	DataDir     string
	LogLevel    string
	AutoMigrate bool

	// Maintenance intervals for scheduled jobs. Zero disables the loop.
	IntegrityInterval   time.Duration
	CleanupInterval     time.Duration
	StoreUpdateInterval time.Duration

	// Store ingestion (phase 7). StoreEnabled gates the provider routes;
	// CredentialKey is the raw GAMEDEPOT_CREDENTIAL_KEY value used to seal stored
	// store credentials, empty when unset.
	StoreEnabled  bool
	CredentialKey string
}

func Load() (Config, error) {
	c := Config{
		Port:                "4000",
		ExternalURL:         "http://localhost:4000",
		LibraryDir:          "./.data/library",
		DataDir:             "./.data/data",
		LogLevel:            "info",
		AutoMigrate:         true,
		IntegrityInterval:   24 * time.Hour,
		CleanupInterval:     time.Hour,
		StoreUpdateInterval: 6 * time.Hour,
	}

	if v := os.Getenv("DATABASE_URL"); v != "" {
		c.DatabaseURL = v
	}
	if v := os.Getenv("PORT"); v != "" {
		c.Port = v
	}
	if v := os.Getenv("EXTERNAL_URL"); v != "" {
		c.ExternalURL = strings.TrimRight(v, "/")
	}
	if v := os.Getenv("LIBRARY"); v != "" {
		c.LibraryDir = v
	}
	if v := os.Getenv("DATA"); v != "" {
		c.DataDir = v
	}
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		c.LogLevel = v
	}
	if v := os.Getenv("GAMEDEPOT_AUTO_MIGRATE"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return c, fmt.Errorf("invalid GAMEDEPOT_AUTO_MIGRATE: %w", err)
		}
		c.AutoMigrate = b
	}
	if v := os.Getenv("GAMEDEPOT_INTEGRITY_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return c, fmt.Errorf("invalid GAMEDEPOT_INTEGRITY_INTERVAL: %w", err)
		}
		c.IntegrityInterval = d
	}
	if v := os.Getenv("GAMEDEPOT_CLEANUP_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return c, fmt.Errorf("invalid GAMEDEPOT_CLEANUP_INTERVAL: %w", err)
		}
		c.CleanupInterval = d
	}
	if v := os.Getenv("GAMEDEPOT_STORE_UPDATE_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return c, fmt.Errorf("invalid GAMEDEPOT_STORE_UPDATE_INTERVAL: %w", err)
		}
		c.StoreUpdateInterval = d
	}
	if v := os.Getenv("GAMEDEPOT_STORE"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return c, fmt.Errorf("invalid GAMEDEPOT_STORE: %w", err)
		}
		c.StoreEnabled = b
	}
	c.CredentialKey = strings.TrimSpace(os.Getenv("GAMEDEPOT_CREDENTIAL_KEY"))
	// Validate the key's shape at boot rather than at the first credential write: the
	// rest of the server treats a missing key as "store off", and a malformed key is
	// indistinguishable from a missing one unless it is checked here.
	if c.CredentialKey != "" {
		if _, err := secrets.ParseKey(c.CredentialKey); err != nil {
			return c, err
		}
	}

	// Store features cannot run without a sealing key: credentials would have to be
	// stored in plaintext. Fail at boot rather than at the first connection save.
	if c.StoreEnabled && c.CredentialKey == "" {
		return c, fmt.Errorf("GAMEDEPOT_STORE requires GAMEDEPOT_CREDENTIAL_KEY (32 bytes, hex or base64)")
	}

	if c.DatabaseURL == "" {
		return c, fmt.Errorf("DATABASE_URL is required")
	}
	if c.ExternalURL == "" || (!strings.HasPrefix(c.ExternalURL, "http://") && !strings.HasPrefix(c.ExternalURL, "https://")) {
		return c, fmt.Errorf("EXTERNAL_URL must start with http:// or https://")
	}
	return c, nil
}
