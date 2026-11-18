// Package maintenance runs the scheduled integrity and cleanup jobs.
//
// Integrity re-verifies every stored release manifest against the depot on disk,
// the same check the dashboard runs per release, so drift (missing or corrupt
// objects) is found before a client trips over it. Cleanup removes pairing codes
// and web sessions past their expiry; object stores are never touched.
package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/cmspeedau/gamedepot/internal/depot"
	db "github.com/cmspeedau/gamedepot/internal/database/db"
)

// Store is the data access the jobs need. *db.Queries satisfies it.
type Store interface {
	ListAllReleases(ctx context.Context) ([]db.ListAllReleasesRow, error)
	DeleteExpiredPairingCodes(ctx context.Context) (int64, error)
	DeleteExpiredSessions(ctx context.Context, expiresAt pgtype.Timestamptz) (int64, error)
}

// IntegrityReport summarizes a verification pass.
type IntegrityReport struct {
	Releases int
	Failed   []string
}

// CleanupReport summarizes an expiry sweep.
type CleanupReport struct {
	PairingCodes int64
	Sessions     int64
}

type Job struct {
	Store     Store
	DepotRoot string
}

func NewJob(store Store, depotRoot string) *Job {
	return &Job{Store: store, DepotRoot: depotRoot}
}

// RunIntegrity verifies every release manifest: digest, format, chunk references,
// and that each referenced object exists and decrypts. A failing release is
// recorded by digest; the pass continues so one bad file does not hide the rest.
func (j *Job) RunIntegrity(ctx context.Context) (IntegrityReport, error) {
	releases, err := j.Store.ListAllReleases(ctx)
	if err != nil {
		return IntegrityReport{}, fmt.Errorf("list releases: %w", err)
	}
	report := IntegrityReport{Releases: len(releases)}
	for _, r := range releases {
		if ctx.Err() != nil {
			return report, ctx.Err()
		}
		var m depot.Manifest
		if err := json.Unmarshal(r.Manifest, &m); err != nil {
			report.Failed = append(report.Failed, r.Digest)
			slog.WarnContext(ctx, "integrity: manifest unreadable", "digest", r.Digest, "err", err)
			continue
		}
		if err := depot.VerifyRelease(j.DepotRoot, m); err != nil {
			report.Failed = append(report.Failed, r.Digest)
			slog.WarnContext(ctx, "integrity: release failed", "digest", r.Digest, "err", err)
		}
	}
	if len(report.Failed) > 0 {
		return report, fmt.Errorf("%d of %d releases failed integrity", len(report.Failed), report.Releases)
	}
	return report, nil
}

// RunCleanup removes expired pairing codes and sessions. It is idempotent; rows
// already gone are simply not counted.
func (j *Job) RunCleanup(ctx context.Context) (CleanupReport, error) {
	var report CleanupReport
	var errs []error

	n, err := j.Store.DeleteExpiredPairingCodes(ctx)
	if err != nil {
		errs = append(errs, fmt.Errorf("pairing codes: %w", err))
	} else {
		report.PairingCodes = n
	}

	n, err = j.Store.DeleteExpiredSessions(ctx, pgtype.Timestamptz{Time: time.Now(), Valid: true})
	if err != nil {
		errs = append(errs, fmt.Errorf("sessions: %w", err))
	} else {
		report.Sessions = n
	}

	return report, errors.Join(errs...)
}
