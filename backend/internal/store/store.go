// Package store holds the provider-neutral shapes that every storefront adapter
// shares. Adapters live in their own subpackages (gog, epic) and are driven by the
// server's ingest task; nothing here talks to a provider.
//
// The contract is deliberately narrow: list what an account owns, and copy one
// product's files into a staging directory. What happens next belongs to this
// project: the staging directory is what internal/depot.BuildRelease already consumes,
// so an acquired title becomes a normal release with no special case in the depot.
package store

import (
	"context"
	"errors"
	"fmt"
)

// ErrReauth means the provider rejected the stored credential. It is not retryable:
// the connection is marked and an admin signs in again.
var ErrReauth = errors.New("store: the provider rejected the credential, sign in again")

// Game is one entitled product.
type Game struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Platforms []string `json:"platforms,omitempty"`
}

// Launch is an executable the provider declares for a product, used to seed the
// version's launch entries on first ingest.
type Launch struct {
	Name string   `json:"name"`
	Exe  string   `json:"exe"`
	Args []string `json:"args,omitempty"`
}

// AcquireOptions narrows what a provider downloads. Empty fields take the provider's
// default (usually windows, en-US, latest build).
type AcquireOptions struct {
	Platform string
	Language string
	// StagingDir is the directory the version tree is written under. The provider
	// creates a subdirectory named after the build's version and fills it.
	StagingDir string
	// OnProgress reports bytes written so far and the total, when known.
	OnProgress func(done, total int64)
}

// Result describes files placed in a staging directory. Paths are relative to Dir and
// use forward slashes.
type Result struct {
	Dir         string
	Files       int
	TotalSize   int64
	BuildID     string
	VersionName string
	Launches    []Launch
}

// BuildInfo is the newest build a provider would acquire for a product.
type BuildInfo struct {
	ID          string
	VersionName string
}

// BuildLister is implemented by providers that can answer "is there anything newer?"
// without downloading. Ingest uses it to skip titles that are already current.
type BuildLister interface {
	LatestBuild(ctx context.Context, productID, platform string) (BuildInfo, error)
}

// Provider acquires titles for one storefront using an already authenticated account.
type Provider interface {
	// Name is the provider key stored in store_connections.provider.
	Name() string
	Library(ctx context.Context) ([]Game, error)
	Acquire(ctx context.Context, productID string, opts AcquireOptions) (Result, error)
}

// ConfigError is a operator-facing failure: bad input or a missing setting, not a
// provider outage. Handlers map it to 400 rather than 502.
type ConfigError struct{ Msg string }

func (e *ConfigError) Error() string { return e.Msg }

func Configf(format string, args ...any) error {
	return &ConfigError{Msg: fmt.Sprintf(format, args...)}
}
