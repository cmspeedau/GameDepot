package maintenance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/cmspeedau/gamedepot/internal/database/db"
	"github.com/cmspeedau/gamedepot/internal/depot"
)

// fakeStore implements Store with canned rows and call counts.
type fakeStore struct {
	releases      []db.ListAllReleasesRow
	pairingCodes  int64
	sessions      int64
	codesCalls    int
	sessionsCalls int
	releasesCalls int
	codesErr      error
	sessionsErr   error
	releasesErr   error
}

func (f *fakeStore) ListAllReleases(ctx context.Context) ([]db.ListAllReleasesRow, error) {
	f.releasesCalls++
	return f.releases, f.releasesErr
}

func (f *fakeStore) DeleteExpiredPairingCodes(ctx context.Context) (int64, error) {
	f.codesCalls++
	return f.pairingCodes, f.codesErr
}

func (f *fakeStore) DeleteExpiredSessions(ctx context.Context, expiresAt pgtype.Timestamptz) (int64, error) {
	f.sessionsCalls++
	return f.sessions, f.sessionsErr
}

// buildRelease writes a valid single-file release into root and returns the manifest bytes
// as the server stores them for the releases row.
func buildRelease(t *testing.T, root string, content []byte) []byte {
	t.Helper()
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "data.bin"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	digest, err := depot.BuildRelease(src, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, "manifests", digest+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRunIntegrityPassesForGoodRelease(t *testing.T) {
	root := t.TempDir()
	mbytes := buildRelease(t, root, []byte("hello world hello world hello world"))

	store := &fakeStore{releases: []db.ListAllReleasesRow{{Digest: "d", Manifest: mbytes}}}
	job := NewJob(store, root)

	report, err := job.RunIntegrity(context.Background())
	if err != nil {
		t.Fatalf("integrity: %v", err)
	}
	if report.Releases != 1 {
		t.Fatalf("releases = %d, want 1", report.Releases)
	}
	if len(report.Failed) != 0 {
		t.Fatalf("failed = %v, want none", report.Failed)
	}
}

func TestRunIntegrityRecordsFailure(t *testing.T) {
	root := t.TempDir()
	mbytes := buildRelease(t, root, []byte("hello world hello world hello world"))
	// Corrupt: replace one byte of the manifest so the digest check fails.
	mbytes[len(mbytes)-2] ^= 0xff

	store := &fakeStore{releases: []db.ListAllReleasesRow{{Digest: "d", Manifest: mbytes}}}
	job := NewJob(store, root)

	report, err := job.RunIntegrity(context.Background())
	if err == nil {
		t.Fatal("want error for failed release")
	}
	if len(report.Failed) != 1 {
		t.Fatalf("failed = %v, want 1", report.Failed)
	}
}

func TestRunIntegrityListsReleasesOnce(t *testing.T) {
	store := &fakeStore{}
	job := NewJob(store, t.TempDir())
	if _, err := job.RunIntegrity(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.releasesCalls != 1 {
		t.Fatalf("ListAllReleases called %d times, want 1", store.releasesCalls)
	}
}

func TestRunCleanupCountsAndPropagatesErrors(t *testing.T) {
	store := &fakeStore{pairingCodes: 2, sessions: 5}
	job := NewJob(store, t.TempDir())
	report, err := job.RunCleanup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.PairingCodes != 2 || report.Sessions != 5 {
		t.Fatalf("report = %+v, want {2 5}", report)
	}
	if store.codesCalls != 1 || store.sessionsCalls != 1 {
		t.Fatalf("calls = codes %d sessions %d, want 1 each", store.codesCalls, store.sessionsCalls)
	}

	store2 := &fakeStore{codesErr: errors.New("boom")}
	if _, err := NewJob(store2, t.TempDir()).RunCleanup(context.Background()); err == nil {
		t.Fatal("want error when pairing code delete fails")
	}
}

func TestRunIntegrityRespectsCancellation(t *testing.T) {
	root := t.TempDir()
	mbytes := buildRelease(t, root, []byte("hello world hello world hello world"))
	// Two identical rows so a second pass happens after cancel would still see them.
	store := &fakeStore{releases: []db.ListAllReleasesRow{{Digest: "d", Manifest: mbytes}, {Digest: "d", Manifest: mbytes}}}
	job := NewJob(store, root)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := job.RunIntegrity(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context canceled", err)
	}
}

func TestLoopCleanupRunsOnInterval(t *testing.T) {
	store := &fakeStore{}
	job := NewJob(store, t.TempDir())
	loop := &Loop{Job: job, Integrity: 50 * time.Millisecond, Cleanup: 20 * time.Millisecond}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		loop.run(ctx)
		close(done)
	}()
	time.Sleep(120 * time.Millisecond)
	cancel()
	<-done

	if store.codesCalls < 2 || store.sessionsCalls < 2 {
		t.Fatalf("cleanup ran codes=%d sessions=%d, want multiple", store.codesCalls, store.sessionsCalls)
	}
	if store.releasesCalls < 1 {
		t.Fatalf("integrity never ran")
	}
}
