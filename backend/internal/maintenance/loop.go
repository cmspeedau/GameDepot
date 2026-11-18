package maintenance

import (
	"context"
	"log/slog"
	"time"
)

// Loop runs a job on fixed intervals until ctx is cancelled.
type Loop struct {
	Job       *Job
	Integrity time.Duration
	Cleanup   time.Duration

	// Now is injectable for tests; nil means time.Now.
	Now func() time.Time
}

// Start launches the loop in a goroutine. It returns immediately. The first
// integrity pass is deferred by the full interval so boot stays fast; cleanup
// runs once at startup and then on its interval.
func (l *Loop) Start(ctx context.Context) {
	go l.run(ctx)
}

func (l *Loop) run(ctx context.Context) {
	interval := l.Integrity
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	cleanup := l.Cleanup
	if cleanup <= 0 {
		cleanup = time.Hour
	}

	l.doCleanup(ctx)

	integrityTicker := time.NewTicker(interval)
	defer integrityTicker.Stop()
	cleanupTicker := time.NewTicker(cleanup)
	defer cleanupTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-integrityTicker.C:
			l.doIntegrity(ctx)
		case <-cleanupTicker.C:
			l.doCleanup(ctx)
		}
	}
}

func (l *Loop) doIntegrity(ctx context.Context) {
	report, err := l.Job.RunIntegrity(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "integrity task", "releases", report.Releases, "failed", len(report.Failed), "err", err)
		return
	}
	slog.InfoContext(ctx, "integrity task complete", "releases", report.Releases)
}

func (l *Loop) doCleanup(ctx context.Context) {
	report, err := l.Job.RunCleanup(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "cleanup task", "err", err)
		return
	}
	slog.InfoContext(ctx, "cleanup task complete", "pairing_codes", report.PairingCodes, "sessions", report.Sessions)
}
