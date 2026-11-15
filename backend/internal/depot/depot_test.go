package depot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildAndVerify(t *testing.T) {
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "a.txt"), []byte("hello world"), 0o644)
	os.MkdirAll(filepath.Join(src, "sub"), 0o755)
	os.WriteFile(filepath.Join(src, "sub", "b.txt"), []byte("second file"), 0o644)

	depotRoot := t.TempDir()
	digest, err := BuildRelease(src, depotRoot, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(digest) != 64 {
		t.Fatalf("bad digest: %s", digest)
	}

	b, err := os.ReadFile(filepath.Join(depotRoot, "manifests", digest+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if err := VerifyRelease(depotRoot, m); err != nil {
		t.Fatalf("verify failed: %v", err)
	}
	for sha := range m.Objects {
		pt, err := DecryptObject(depotRoot, m, sha)
		if err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256(pt)
		if hex.EncodeToString(h[:]) != sha {
			t.Fatalf("plaintext hash mismatch")
		}
	}
	canon, err := canonicalManifest(m, false)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(canon)
	if hex.EncodeToString(h[:]) != m.Digest {
		t.Fatalf("digest mismatch")
	}
	if len(m.Files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(m.Files))
	}
}
