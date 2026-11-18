// Package ingest turns a store product into a local release.
//
// It is deliberately thin: a provider copies the product's files into a staging
// directory, and the existing depot builder consumes that directory exactly as it
// consumes any library source. Nothing downstream knows or cares that a title came
// from a store rather than from disk, which is what keeps the provider edge small.
package ingest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/cmspeedau/gamedepot/internal/database/db"
	"github.com/cmspeedau/gamedepot/internal/depot"
	"github.com/cmspeedau/gamedepot/internal/store"
)

// Options describes one ingest request.
type Options struct {
	Provider string
	// ProductID is the provider-side identifier (GOG product id, Epic catalogue item id).
	ProductID string
	// Title overrides the provider title when the caller already knows it.
	Title string
	// LinkID ties the ingest to a store_products row; when empty and the product has
	// never been seen, the game row is created.
	Platform string
	Language string
	// StagingRoot holds the temporary tree; it is removed when the run finishes.
	StagingRoot string
	// DepotRoot is the object store the release is written to.
	DepotRoot string
	// PublisherID is the admin who triggered the ingest.
	PublisherID pgtype.UUID
	// Progress receives (done, total) bytes during acquisition and chunking.
	Progress func(done, total int64)
	// Note overrides the stored title or forces a re-ingest past the up-to-date check.
	Force bool
}

// Result reports what the run produced. Reused is true when the product was already
// current and nothing was downloaded.
type Result struct {
	GameID      uuid.UUID
	VersionID   uuid.UUID
	Digest      string
	BuildID     string
	VersionName string
	Files       int
	Bytes       int64
	Reused      bool
}

// Store is the data access ingest needs. *db.Queries satisfies it, and the tests use a
// recorder, following the same narrow-interface style as internal/maintenance.
type Store interface {
	GetStoreProduct(ctx context.Context, arg db.GetStoreProductParams) (db.StoreProduct, error)
	GetStoreProductSiblings(ctx context.Context, arg db.GetStoreProductSiblingsParams) ([]db.StoreProduct, error)
	GetGameByID(ctx context.Context, id pgtype.UUID) (db.Game, error)
	CreateGame(ctx context.Context, arg db.CreateGameParams) (db.Game, error)
	CreateGameVersion(ctx context.Context, arg db.CreateGameVersionParams) (db.GameVersion, error)
	CreateLaunch(ctx context.Context, arg db.CreateLaunchParams) (db.Launch, error)
	CreateRelease(ctx context.Context, arg db.CreateReleaseParams) (db.CreateReleaseRow, error)
	UpsertStoreProduct(ctx context.Context, arg db.UpsertStoreProductParams) (db.StoreProduct, error)
}

// Service runs ingests against one database and depot.
type Service struct {
	q       Store
	staging string
	depot   string
}

func NewService(q Store, stagingRoot, depotRoot string) *Service {
	return &Service{q: q, staging: stagingRoot, depot: depotRoot}
}

// Run acquires one product and publishes it. The provider is passed in rather than
// constructed here so the caller controls credentials and the test can inject a fake.
func (s *Service) Run(ctx context.Context, p store.Provider, opts Options) (Result, error) {
	var out Result
	if p == nil {
		return out, errors.New("ingest: no provider")
	}
	if opts.ProductID == "" {
		return out, store.Configf("a product id is required")
	}
	platform := opts.Platform
	if platform == "" {
		platform = "windows"
	}
	stagingRoot := opts.StagingRoot
	if stagingRoot == "" {
		stagingRoot = s.staging
	}
	depotRoot := opts.DepotRoot
	if depotRoot == "" {
		depotRoot = s.depot
	}
	if stagingRoot == "" || depotRoot == "" {
		return out, store.Configf("a staging directory and a depot root are required")
	}

	product, hadProduct := s.existingProduct(ctx, p.Name(), opts.ProductID, platform)
	siblings := s.productSiblings(ctx, p.Name(), opts.ProductID)

	// Skip the download when the recorded build is still the newest. A store title that
	// has not changed should cost nothing to check.
	if !opts.Force && hadProduct && product.LastBuild.Valid && product.LastBuild.String != "" {
		if latest, ok := p.(store.BuildLister); ok {
			info, err := latest.LatestBuild(ctx, opts.ProductID, platform)
			if err == nil && info.ID == product.LastBuild.String {
				out.Reused = true
				out.BuildID = info.ID
				out.GameID = uuid.UUID(product.GameID.Bytes)
				out.Digest = stringOrEmpty(product.LastReleaseDigest)
				slog.InfoContext(ctx, "ingest up to date",
					"provider", p.Name(), "product", opts.ProductID, "build", info.ID)
				return out, nil
			}
		}
	}

	jobID := uuid.New()
	dir := filepath.Join(stagingRoot, jobID.String())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return out, fmt.Errorf("ingest: staging: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			slog.WarnContext(ctx, "ingest: staging cleanup", "dir", dir, "err", err)
		}
	}()

	acquired, err := p.Acquire(ctx, opts.ProductID, store.AcquireOptions{
		Platform:   platform,
		Language:   opts.Language,
		StagingDir: dir,
		OnProgress: opts.Progress,
	})
	if err != nil {
		return out, err
	}
	if acquired.Files == 0 {
		return out, fmt.Errorf("ingest: %s acquired no files for product %s", p.Name(), opts.ProductID)
	}

	// The staging tree may nest content under a provider install directory; the release
	// must contain the game files themselves, so a single top-level directory is
	// unwrapped before chunking.
	source := unwrapSingleDir(acquired.Dir)

	digest, err := depot.BuildRelease(source, depotRoot, func(done, total int64) {
		if opts.Progress != nil {
			opts.Progress(done, total)
		}
	})
	if err != nil {
		return out, fmt.Errorf("ingest: build release: %w", err)
	}
	manifest, err := os.ReadFile(filepath.Join(depotRoot, "manifests", digest+".json"))
	if err != nil {
		return out, fmt.Errorf("ingest: read manifest: %w", err)
	}

	gameID, title, err := s.ensureGame(ctx, product, hadProduct, opts, siblings)
	if err != nil {
		return out, err
	}

	versionName := acquired.VersionName
	if versionName == "" {
		versionName = acquired.BuildID
	}
	versionID := uuid.New()
	if _, err := s.q.CreateGameVersion(ctx, db.CreateGameVersionParams{
		ID:        pgtype.UUID{Bytes: versionID, Valid: true},
		GameID:    pgtype.UUID{Bytes: gameID, Valid: true},
		Name:      versionName,
		Platform:  platform,
		TotalSize: acquired.TotalSize,
	}); err != nil {
		return out, fmt.Errorf("ingest: create version: %w", err)
	}

	for _, launch := range acquired.Launches {
		if launch.Exe == "" {
			continue
		}
		if _, err := s.q.CreateLaunch(ctx, db.CreateLaunchParams{
			ID:         pgtype.UUID{Bytes: uuid.New(), Valid: true},
			VersionID:  pgtype.UUID{Bytes: versionID, Valid: true},
			Platform:   platform,
			Name:       firstNonEmpty(launch.Name, "Play"),
			Executable: launch.Exe,
			Arguments:  launch.Args,
		}); err != nil {
			slog.WarnContext(ctx, "ingest: launch entry rejected",
				"version", versionID.String(), "exe", launch.Exe, "err", err)
		}
	}

	if _, err := s.q.CreateRelease(ctx, db.CreateReleaseParams{
		ID:          pgtype.UUID{Bytes: uuid.New(), Valid: true},
		GameID:      pgtype.UUID{Bytes: gameID, Valid: true},
		VersionID:   pgtype.UUID{Bytes: versionID, Valid: true},
		Digest:      digest,
		Manifest:    manifest,
		TotalSize:   acquired.TotalSize,
		PublishedBy: opts.PublisherID,
	}); err != nil {
		return out, fmt.Errorf("ingest: create release: %w", err)
	}

	productRowID := uuid.New()
	if product.ID.Valid {
		productRowID = uuid.UUID(product.ID.Bytes)
	}
	if _, err := s.q.UpsertStoreProduct(ctx, db.UpsertStoreProductParams{
		ID:                pgtype.UUID{Bytes: productRowID, Valid: true},
		Provider:          p.Name(),
		ProductID:         opts.ProductID,
		GameID:            pgtype.UUID{Bytes: gameID, Valid: true},
		Title:             title,
		Platform:          platform,
		LastBuild:         pgtype.Text{String: acquired.BuildID, Valid: acquired.BuildID != ""},
		LastReleaseDigest: pgtype.Text{String: digest, Valid: true},
	}); err != nil {
		// The release exists; losing only the mapping means the next check re-ingests.
		slog.WarnContext(ctx, "ingest: product mapping not saved", "product", opts.ProductID, "err", err)
	}

	out = Result{
		GameID:      gameID,
		VersionID:   versionID,
		Digest:      digest,
		BuildID:     acquired.BuildID,
		VersionName: versionName,
		Files:       acquired.Files,
		Bytes:       acquired.TotalSize,
	}
	slog.InfoContext(ctx, "ingest complete",
		"provider", p.Name(), "product", opts.ProductID, "game", gameID.String(),
		"version", versionName, "digest", digest, "files", out.Files, "bytes", out.Bytes)
	return out, nil
}

// productSiblings lists every platform mapping recorded for this provider product.
// Failing to read them is not fatal: the worst case is a fresh game row.
func (s *Service) productSiblings(ctx context.Context, provider, productID string) []db.StoreProduct {
	rows, err := s.q.GetStoreProductSiblings(ctx, db.GetStoreProductSiblingsParams{
		Provider:  provider,
		ProductID: productID,
	})
	if err != nil {
		return nil
	}
	return rows
}

func (s *Service) existingProduct(ctx context.Context, provider, productID, platform string) (db.StoreProduct, bool) {
	row, err := s.q.GetStoreProduct(ctx, db.GetStoreProductParams{
		Provider:  provider,
		ProductID: productID,
		Platform:  platform,
	})
	if err != nil {
		return db.StoreProduct{}, false
	}
	return row, true
}

// ensureGame reuses the mapped game when one exists, and creates the catalog row on
// first sight of the product.
// ensureGame resolves which game row this release belongs to.
//
// A title ingested for two platforms is still one game: the product mapping is keyed by
// (provider, product, platform), so the platform's own row can be absent while a sibling
// platform already created the game. Following the sibling keeps one catalogue entry with
// one version per build, which is what the desktop expects to see.
func (s *Service) ensureGame(ctx context.Context, product db.StoreProduct, hadProduct bool, opts Options, siblings []db.StoreProduct) (uuid.UUID, string, error) {
	title := firstNonEmpty(opts.Title, product.Title)
	if title == "" {
		for _, sib := range siblings {
			if sib.Title != "" {
				title = sib.Title
				break
			}
		}
	}
	if title == "" {
		title = fmt.Sprintf("%s %s", opts.Provider, opts.ProductID)
	}
	if hadProduct && product.GameID.Valid {
		if _, err := s.q.GetGameByID(ctx, product.GameID); err == nil {
			return uuid.UUID(product.GameID.Bytes), firstNonEmpty(title, product.Title), nil
		}
		// The game was deleted since; fall through to a sibling or a fresh row.
	}
	for _, sib := range siblings {
		if !sib.GameID.Valid {
			continue
		}
		if _, err := s.q.GetGameByID(ctx, sib.GameID); err == nil {
			// The other platform's row carries the real title, which the caller may
			// not have supplied for this platform.
			return uuid.UUID(sib.GameID.Bytes), firstNonEmpty(title, sib.Title), nil
		}
	}
	gameID := uuid.New()
	slug := Slugify(title)
	if _, err := s.q.CreateGame(ctx, db.CreateGameParams{
		ID:   pgtype.UUID{Bytes: gameID, Valid: true},
		Slug: slug,
		Name: title,
	}); err != nil {
		return uuid.Nil, "", fmt.Errorf("ingest: create game %q: %w", title, err)
	}
	return gameID, title, nil
}

func stringOrEmpty(t pgtype.Text) string {
	if t.Valid {
		return t.String
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// unwrapSingleDir descends through a chain of single directories, which is how GOG and
// Epic nest content, so the release stores game files at the path players expect
// rather than under one wrapper folder.
func unwrapSingleDir(root string) string {
	current := root
	for i := 0; i < 4; i++ {
		entries, err := os.ReadDir(current)
		if err != nil || len(entries) != 1 || !entries[0].IsDir() {
			return current
		}
		next := filepath.Join(current, entries[0].Name())
		// Stop if there are loose files beside the directory: the wrapper is real content.
		if len(entries) != 1 {
			return current
		}
		current = next
	}
	return current
}

var slugNonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify makes a title usable as a game slug. Exported because the dashboard previews
// the slug before creating anything.
func Slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugNonAlnum.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "untitled"
	}
	return s
}
