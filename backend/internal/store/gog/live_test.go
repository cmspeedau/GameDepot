package gog_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cmspeedau/gamedepot/internal/store"
	"github.com/cmspeedau/gamedepot/internal/store/gog"
)

// Live check against production GOG, gated behind GAMEDEPOT_LIVE_GOG=1.
//
// It exists because of a specific bug: the ingest pipeline asked GOG for
// /os/macos/builds, GOG only knows /os/osx/builds, and every Mac and Linux title
// silently came back as "no builds". GOG answers this endpoint without a valid
// token, so the mapping, the build meta and the depot manifest can all be proven
// against the real service with no account involved. Acquisition itself stops at
// secure_link, which does need one.
//
// The product is The Witcher 2 Enhanced Edition (1207658930), used here because it
// genuinely ships Windows and Mac builds and has for years.
func TestLiveGOGMacBuildsResolve(t *testing.T) {
	if os.Getenv("GAMEDEPOT_LIVE_GOG") != "1" {
		t.Skip("set GAMEDEPOT_LIVE_GOG=1 to check against production GOG")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	const product = "1207658930"
	client := gog.NewClient(gog.DefaultConfig(), gog.Tokens{AccessToken: "unauthenticated-probe"})

	// The caller's word for Mac has to reach GOG as "osx".
	if got, err := gog.ContentOS("macos"); err != nil || got != "osx" {
		t.Fatalf("ContentOS(macos) = %q, %v", got, err)
	}

	builds, err := client.Builds(ctx, product, "macos")
	if err != nil {
		t.Fatalf("mac builds: %v", err)
	}
	if len(builds) == 0 {
		t.Fatal("GOG returned no Mac builds: the osx mapping is broken again")
	}
	if builds[0].OS != "osx" {
		t.Errorf("newest Mac build reports os=%q, want osx", builds[0].OS)
	}

	meta, err := client.BuildMeta(ctx, builds[0].Link)
	if err != nil {
		t.Fatalf("build meta: %v", err)
	}
	if meta.Platform != "osx" {
		t.Errorf("meta platform = %q, want osx", meta.Platform)
	}
	if len(meta.Depots) == 0 {
		t.Fatal("no depots in the Mac build meta")
	}

	var manifestHash string
	for _, d := range meta.Depots {
		if d.Manifest != "" {
			manifestHash = d.Manifest
			break
		}
	}
	files, err := client.DepotManifest(ctx, manifestHash)
	if err != nil {
		t.Fatalf("depot manifest: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("the Mac depot manifest listed no files")
	}

	// Real Mac content, not Windows content under a Mac label.
	macMarker := false
	for _, f := range files {
		if strings.Contains(f.Path, ".app/Contents/MacOS/") || strings.EqualFold(f.Path, "start.command") {
			macMarker = true
			break
		}
	}
	if !macMarker {
		t.Errorf("no Mac bundle paths among the first %d files; sample: %s", len(files), samplePaths(files))
	}
	t.Logf("mac build %s version %q: %d depots, %d files", builds[0].BuildID, builds[0].VersionName, len(meta.Depots), len(files))
}

func TestLiveGOGWindowsBuildsStillResolve(t *testing.T) {
	if os.Getenv("GAMEDEPOT_LIVE_GOG") != "1" {
		t.Skip("set GAMEDEPOT_LIVE_GOG=1 to check against production GOG")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	client := gog.NewClient(gog.DefaultConfig(), gog.Tokens{AccessToken: "unauthenticated-probe"})
	builds, err := client.Builds(ctx, "1207658930", "windows")
	if err != nil || len(builds) == 0 {
		t.Fatalf("windows builds: %d found, err %v", len(builds), err)
	}
	meta, err := client.BuildMeta(ctx, builds[0].Link)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Platform != "windows" {
		t.Fatalf("windows build labelled %q", meta.Platform)
	}
}

// TestLiveGOGLinuxIsRefusedBeforeTheNetwork documents the other half of the original
// complaint: GOG's content system genuinely has no Linux builds, so the adapter must
// say that instead of quietly downloading Windows files.
func TestLiveGOGLinuxIsRefusedBeforeTheNetwork(t *testing.T) {
	var cfgErr *store.ConfigError
	_, err := gog.NewClient(gog.DefaultConfig(), gog.Tokens{}).Builds(context.Background(), "1207658930", "linux")
	if err == nil {
		t.Fatal("want the refusal")
	}
	if !errors.As(err, &cfgErr) {
		t.Fatalf("err = %T %v, want a config error raised before any request", err, err)
	}
}

func samplePaths(files []gog.DepotFile) string {
	var out []string
	for i, f := range files {
		if i >= 4 {
			break
		}
		out = append(out, f.Path)
	}
	return strings.Join(out, ", ")
}
