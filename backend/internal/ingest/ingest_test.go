package ingest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/cmspeedau/gamedepot/internal/database/db"
	"github.com/cmspeedau/gamedepot/internal/store"
)

// fakeProvider writes a small tree instead of downloading, and records the options it
// was called with.
type fakeProvider struct {
	name     string
	files    map[string][]byte
	buildID  string
	version  string
	launches []store.Launch
	gotOpts  store.AcquireOptions
	acqErr   error
	calls    int
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) Library(ctx context.Context) ([]store.Game, error) {
	return []store.Game{{ID: "p1", Title: "Example Game"}}, nil
}

func (f *fakeProvider) Acquire(ctx context.Context, productID string, opts store.AcquireOptions) (store.Result, error) {
	f.calls++
	f.gotOpts = opts
	if f.acqErr != nil {
		return store.Result{}, f.acqErr
	}
	base := filepath.Join(opts.StagingDir, f.version, "Game")
	if err := os.MkdirAll(base, 0o755); err != nil {
		return store.Result{}, err
	}
	var total int64
	for path, content := range f.files {
		target := filepath.Join(base, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return store.Result{}, err
		}
		if err := os.WriteFile(target, content, 0o644); err != nil {
			return store.Result{}, err
		}
		total += int64(len(content))
	}
	if opts.OnProgress != nil {
		opts.OnProgress(total, total)
	}
	return store.Result{
		Dir:         filepath.Join(opts.StagingDir, f.version),
		Files:       len(f.files),
		TotalSize:   total,
		BuildID:     f.buildID,
		VersionName: f.version,
		Launches:    f.launches,
	}, nil
}

func (f *fakeProvider) LatestBuild(ctx context.Context, productID, platform string) (store.BuildInfo, error) {
	return store.BuildInfo{ID: f.buildID, VersionName: f.version}, nil
}

// recorder captures the writes ingest makes; reads answer "no product yet" unless the
// test seeds one.
type recorder struct {
	existing    *db.StoreProduct
	siblings    []db.StoreProduct
	siblingErr  error
	productErr  error
	gameErr     error
	versionErr  error
	releaseErr  error
	launchErr   error
	upsertErr   error
	createdGame db.CreateGameParams
	versions    []db.CreateGameVersionParams
	launches    []db.CreateLaunchParams
	releases    []db.CreateReleaseParams
	upserts     []db.UpsertStoreProductParams
	reads       int
	gameLookups int
}

func (r *recorder) GetStoreProduct(ctx context.Context, arg db.GetStoreProductParams) (db.StoreProduct, error) {
	r.reads++
	if r.productErr != nil {
		return db.StoreProduct{}, r.productErr
	}
	if r.existing != nil {
		return *r.existing, nil
	}
	return db.StoreProduct{}, pgx.ErrNoRows
}

func (r *recorder) GetStoreProductSiblings(ctx context.Context, arg db.GetStoreProductSiblingsParams) ([]db.StoreProduct, error) {
	if r.siblingErr != nil {
		return nil, r.siblingErr
	}
	return r.siblings, nil
}

func (r *recorder) GetGameByID(ctx context.Context, id pgtype.UUID) (db.Game, error) {
	r.gameLookups++
	if r.gameErr != nil {
		return db.Game{}, r.gameErr
	}
	return db.Game{ID: id}, nil
}

func (r *recorder) CreateGame(ctx context.Context, arg db.CreateGameParams) (db.Game, error) {
	r.createdGame = arg
	return db.Game{ID: arg.ID, Slug: arg.Slug, Name: arg.Name}, nil
}

func (r *recorder) CreateGameVersion(ctx context.Context, arg db.CreateGameVersionParams) (db.GameVersion, error) {
	if r.versionErr != nil {
		return db.GameVersion{}, r.versionErr
	}
	r.versions = append(r.versions, arg)
	return db.GameVersion{ID: arg.ID, Name: arg.Name, Platform: arg.Platform}, nil
}

func (r *recorder) CreateLaunch(ctx context.Context, arg db.CreateLaunchParams) (db.Launch, error) {
	if r.launchErr != nil {
		return db.Launch{}, r.launchErr
	}
	r.launches = append(r.launches, arg)
	return db.Launch{ID: arg.ID, Name: arg.Name, Executable: arg.Executable}, nil
}

func (r *recorder) CreateRelease(ctx context.Context, arg db.CreateReleaseParams) (db.CreateReleaseRow, error) {
	if r.releaseErr != nil {
		return db.CreateReleaseRow{}, r.releaseErr
	}
	r.releases = append(r.releases, arg)
	return db.CreateReleaseRow{ID: arg.ID, Digest: arg.Digest}, nil
}

func (r *recorder) UpsertStoreProduct(ctx context.Context, arg db.UpsertStoreProductParams) (db.StoreProduct, error) {
	if r.upsertErr != nil {
		return db.StoreProduct{}, r.upsertErr
	}
	r.upserts = append(r.upserts, arg)
	return db.StoreProduct{ID: arg.ID, ProductID: arg.ProductID, LastBuild: arg.LastBuild}, nil
}

func newGameProvider() *fakeProvider {
	return &fakeProvider{
		name: "gog", buildID: "b1", version: "1.5",
		files: map[string][]byte{
			"play.exe":     []byte("binary-bytes"),
			"data/one.txt": []byte("one"),
		},
		launches: []store.Launch{
			{Name: "Play", Exe: "play.exe"},
			{Name: "Editor", Exe: "tools/editor.exe", Args: []string{"--edit"}},
		},
	}
}

func TestRunPublishesAcquiredTree(t *testing.T) {
	rec := &recorder{}
	staging := t.TempDir()
	depotRoot := t.TempDir()
	svc := NewService(rec, staging, depotRoot)
	provider := newGameProvider()

	var progressCalled bool
	result, err := svc.Run(context.Background(), provider, Options{
		Provider: "gog", ProductID: "1711765822", Title: "Timberborn",
		Platform: "windows", StagingRoot: staging, DepotRoot: depotRoot,
		Progress: func(done, total int64) { progressCalled = true },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Reused || result.Digest == "" || result.VersionName != "1.5" {
		t.Fatalf("result = %+v", result)
	}
	if result.Files != 2 {
		t.Fatalf("files = %d", result.Files)
	}
	if !progressCalled {
		t.Error("the progress callback was never called")
	}

	// The provider got the staging directory inside our root, not the root itself.
	if provider.gotOpts.StagingDir == staging || provider.gotOpts.StagingDir == "" {
		t.Errorf("staging dir = %q, want a job directory under %q", provider.gotOpts.StagingDir, staging)
	}

	// A game row is created with a slug derived from the title.
	if rec.createdGame.Name != "Timberborn" {
		t.Fatalf("game = %+v", rec.createdGame)
	}
	if got := string(rec.createdGame.Slug); got != "timberborn" {
		t.Fatalf("slug = %q", got)
	}
	if len(rec.versions) != 1 || rec.versions[0].Name != "1.5" || rec.versions[0].Platform != "windows" {
		t.Fatalf("versions = %+v", rec.versions)
	}
	if len(rec.releases) != 1 || len(rec.releases[0].Manifest) == 0 {
		t.Fatalf("releases = %+v", rec.releases)
	}
	// The manifest stored on the release row is the one the depot wrote.
	if _, err := os.Stat(filepath.Join(depotRoot, "manifests", result.Digest+".json")); err != nil {
		t.Fatalf("depot manifest missing: %v", err)
	}

	// Launches land with their arguments, executable paths as the provider declared.
	if len(rec.launches) != 2 {
		t.Fatalf("launches = %+v", rec.launches)
	}
	if rec.launches[1].Executable != "tools/editor.exe" || strings.Join(rec.launches[1].Arguments, " ") != "--edit" {
		t.Fatalf("launch = %+v", rec.launches[1])
	}
	if rec.launches[0].Name != "Play" {
		t.Fatalf("launch name = %q", rec.launches[0].Name)
	}

	// The product mapping records the build so the next check is cheap.
	if len(rec.upserts) != 1 || rec.upserts[0].LastBuild.String != "b1" {
		t.Fatalf("upserts = %+v", rec.upserts)
	}
	// Staging is gone; the depot keeps the content.
	leftovers, _ := os.ReadDir(staging)
	if len(leftovers) != 0 {
		t.Fatalf("staging not cleaned: %v", leftovers)
	}
}

func TestRunSkipsUnchangedBuild(t *testing.T) {
	storedID := uuid.New()
	gameID := uuid.New()
	rec := &recorder{existing: &db.StoreProduct{
		ID:                pgtype.UUID{Bytes: storedID, Valid: true},
		ProductID:         "1711765822",
		GameID:            pgtype.UUID{Bytes: gameID, Valid: true},
		Title:             "Timberborn",
		Platform:          "windows",
		LastBuild:         pgtype.Text{String: "b1", Valid: true},
		LastReleaseDigest: pgtype.Text{String: "deadbeef", Valid: true},
	}}
	provider := newGameProvider() // LatestBuild answers b1
	svc := NewService(rec, t.TempDir(), t.TempDir())

	result, err := svc.Run(context.Background(), provider, Options{
		Provider: "gog", ProductID: "1711765822", Platform: "windows",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Reused {
		t.Fatalf("result = %+v, want Reused", result)
	}
	if result.Digest != "deadbeef" {
		t.Fatalf("digest = %q, want the stored one", result.Digest)
	}
	if provider.calls != 0 {
		t.Fatalf("provider acquired %d times, want 0", provider.calls)
	}
	if len(rec.releases) != 0 {
		t.Fatal("a reused product must not create a release")
	}
}

func TestForceReingestsKnownBuild(t *testing.T) {
	gameID := uuid.New()
	rec := &recorder{existing: &db.StoreProduct{
		ID:        pgtype.UUID{Bytes: uuid.New(), Valid: true},
		GameID:    pgtype.UUID{Bytes: gameID, Valid: true},
		ProductID: "1711765822",
		Title:     "Timberborn",
		Platform:  "windows",
		LastBuild: pgtype.Text{String: "b1", Valid: true},
	}}
	provider := newGameProvider()
	svc := NewService(rec, t.TempDir(), t.TempDir())

	result, err := svc.Run(context.Background(), provider, Options{
		Provider: "gog", ProductID: "1711765822", Platform: "windows", Force: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Reused || provider.calls != 1 {
		t.Fatalf("result = %+v calls = %d, want a real re-ingest", result, provider.calls)
	}
	// The existing game row is reused rather than recreated.
	if rec.createdGame.Name != "" {
		t.Fatal("CreateGame was called for a product already mapped to a game")
	}
	if len(rec.releases) != 1 {
		t.Fatalf("releases = %d", len(rec.releases))
	}
}

func TestDeletedGameIsRecreated(t *testing.T) {
	rec := &recorder{
		existing: &db.StoreProduct{
			ID:        pgtype.UUID{Bytes: uuid.New(), Valid: true},
			GameID:    pgtype.UUID{Bytes: uuid.New(), Valid: true},
			ProductID: "1711765822",
			Title:     "Timberborn",
			Platform:  "windows",
		},
		gameErr: pgx.ErrNoRows, // the mapped game is gone
	}
	svc := NewService(rec, t.TempDir(), t.TempDir())
	if _, err := svc.Run(context.Background(), newGameProvider(), Options{
		Provider: "gog", ProductID: "1711765822", Platform: "windows",
	}); err != nil {
		t.Fatal(err)
	}
	if rec.createdGame.Name != "Timberborn" {
		t.Fatalf("want a fresh game row, got %+v", rec.createdGame)
	}
}

func TestLaunchFailureDoesNotLoseTheRelease(t *testing.T) {
	rec := &recorder{launchErr: errors.New("launch insert rejected")}
	svc := NewService(rec, t.TempDir(), t.TempDir())
	result, err := svc.Run(context.Background(), newGameProvider(), Options{
		Provider: "gog", ProductID: "1711765822", Title: "Timberborn", Platform: "windows",
	})
	if err != nil {
		t.Fatalf("a bad launch row must not fail the ingest: %v", err)
	}
	if len(rec.releases) != 1 || result.Digest == "" {
		t.Fatalf("releases = %d result = %+v", len(rec.releases), result)
	}
	if len(rec.launches) != 0 {
		t.Fatal("no launches should be recorded when the insert fails")
	}
}

// The product mapping is keyed per platform, so ingesting the Mac build of a title
// already ingested for Windows must extend the same game rather than fork a second
// catalogue entry with the same name.
func TestOtherPlatformBuildExtendsTheSameGame(t *testing.T) {
	gameID := uuid.New()
	rec := &recorder{
		// no mapping for this platform yet...
		existing: nil,
		// ...but Windows already created the game for the same product
		siblings: []db.StoreProduct{{
			ID:        pgtype.UUID{Bytes: uuid.New(), Valid: true},
			ProductID: "1711765822",
			GameID:    pgtype.UUID{Bytes: gameID, Valid: true},
			Title:     "Timberborn",
			Platform:  "windows",
		}},
	}
	svc := NewService(rec, t.TempDir(), t.TempDir())
	result, err := svc.Run(context.Background(), newGameProvider(), Options{
		Provider: "gog", ProductID: "1711765822", Platform: "macos",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.GameID != gameID {
		t.Fatalf("game = %s, want the existing %s", result.GameID, gameID)
	}
	if rec.createdGame.Name != "" {
		t.Fatalf("a second game row was created: %+v", rec.createdGame)
	}
	// The version is still recorded, on the Mac platform, with the title carried over
	// from the sibling because the caller did not pass one.
	if len(rec.versions) != 1 || rec.versions[0].Platform != "macos" {
		t.Fatalf("versions = %+v", rec.versions)
	}
	if len(rec.upserts) != 1 || rec.upserts[0].Platform != "macos" || rec.upserts[0].Title != "Timberborn" {
		t.Fatalf("upserts = %+v", rec.upserts)
	}
}

func TestUnreadableSiblingsDoNotStopTheIngest(t *testing.T) {
	rec := &recorder{siblingErr: errors.New("read failed")}
	svc := NewService(rec, t.TempDir(), t.TempDir())
	result, err := svc.Run(context.Background(), newGameProvider(), Options{
		Provider: "gog", ProductID: "1711765822", Title: "Timberborn", Platform: "macos",
	})
	if err != nil {
		t.Fatalf("a sibling read failure must not fail the run: %v", err)
	}
	if rec.createdGame.Name != "Timberborn" {
		t.Fatalf("game = %+v, want a fresh row from the supplied title", rec.createdGame)
	}
	if result.GameID == uuid.Nil {
		t.Fatal("want a game id")
	}
}

func TestProductMappingFailureStillReportsSuccess(t *testing.T) {
	// The release exists; losing only the mapping costs an extra download next time.
	rec := &recorder{upsertErr: errors.New("mapping write failed")}
	svc := NewService(rec, t.TempDir(), t.TempDir())
	if _, err := svc.Run(context.Background(), newGameProvider(), Options{
		Provider: "gog", ProductID: "1711765822", Title: "Timberborn", Platform: "windows",
	}); err != nil {
		t.Fatalf("err = %v, want the ingest to succeed", err)
	}
}

func TestVersionFailurePropagates(t *testing.T) {
	rec := &recorder{versionErr: errors.New("version write failed")}
	svc := NewService(rec, t.TempDir(), t.TempDir())
	if _, err := svc.Run(context.Background(), newGameProvider(), Options{
		Provider: "gog", ProductID: "1711765822", Title: "Timberborn", Platform: "windows",
	}); err == nil {
		t.Fatal("want the version error to stop the run")
	}
}

func TestProviderErrorPropagates(t *testing.T) {
	svc := NewService(&recorder{}, t.TempDir(), t.TempDir())
	provider := newGameProvider()
	provider.acqErr = errors.New("gog: the download secure link was rejected")
	_, err := svc.Run(context.Background(), provider, Options{ProductID: "1711765822"})
	if err == nil || !strings.Contains(err.Error(), "secure link") {
		t.Fatalf("err = %v", err)
	}
}

func TestEmptyAcquisitionFails(t *testing.T) {
	svc := NewService(&recorder{}, t.TempDir(), t.TempDir())
	provider := newGameProvider()
	provider.files = nil
	if _, err := svc.Run(context.Background(), provider, Options{ProductID: "1711765822"}); err == nil {
		t.Fatal("an acquisition that produced no files must fail")
	}
}

func TestMissingArgumentsAreConfigErrors(t *testing.T) {
	svc := NewService(&recorder{}, t.TempDir(), t.TempDir())
	if _, err := svc.Run(context.Background(), newGameProvider(), Options{}); err == nil {
		t.Fatal("want a config error for a missing product id")
	}
	if _, err := svc.Run(context.Background(), nil, Options{ProductID: "p"}); err == nil {
		t.Fatal("want an error for a nil provider")
	}
	bad := NewService(&recorder{}, "", "")
	if _, err := bad.Run(context.Background(), newGameProvider(), Options{ProductID: "p"}); err == nil {
		t.Fatal("want a config error when staging and depot roots are unset")
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Timberborn":                "timberborn",
		"  Heroic Engine: Deluxe! ": "heroic-engine-deluxe",
		"Two  Spaces":               "two-spaces",
		"***":                       "untitled",
		"":                          "untitled",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUnwrapSingleDir(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "1.5", "Game")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "play.exe"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := filepath.Base(unwrapSingleDir(filepath.Join(root, "1.5"))); got != "Game" {
		t.Fatalf("unwrapped to %q, want the innermost single directory", got)
	}

	// A directory with a sibling file is real content and must not be unwrapped.
	other := t.TempDir()
	if err := os.MkdirAll(filepath.Join(other, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "readme.txt"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := unwrapSingleDir(other); got != other {
		t.Fatalf("unwrapped %q to %q, want it left alone", other, got)
	}
}
