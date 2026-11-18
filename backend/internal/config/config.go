package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
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
	IntegrityInterval time.Duration
	CleanupInterval   time.Duration
}

func Load() (Config, error) {
	c := Config{
		Port:              "4000",
		ExternalURL:       "http://localhost:4000",
		LibraryDir:        "./.data/library",
		DataDir:           "./.data/data",
		LogLevel:          "info",
		AutoMigrate:       true,
		IntegrityInterval: 24 * time.Hour,
		CleanupInterval:   time.Hour,
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

	if c.DatabaseURL == "" {
		return c, fmt.Errorf("DATABASE_URL is required")
	}
	if c.ExternalURL == "" || (!strings.HasPrefix(c.ExternalURL, "http://") && !strings.HasPrefix(c.ExternalURL, "https://")) {
		return c, fmt.Errorf("EXTERNAL_URL must start with http:// or https://")
	}
	return c, nil
}
