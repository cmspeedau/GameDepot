package saves

import "testing"

func TestContentHashIsStableAndOrderIndependent(t *testing.T) {
	a := []FileEntry{
		{Key: "b.cfg", Size: 2, SHA256: "bbb", Mode: 0o644},
		{Key: "a.cfg", Size: 1, SHA256: "aaa", Mode: 0o600},
	}
	b := []FileEntry{
		{Key: "a.cfg", Size: 1, SHA256: "aaa", Mode: 0o600},
		{Key: "b.cfg", Size: 2, SHA256: "bbb", Mode: 0o644},
	}
	if ContentHash(a) != ContentHash(b) {
		t.Fatal("content hash must not depend on input order")
	}
	// Empty set has a defined hash (sha256 of empty).
	if ContentHash(nil) != ContentHash([]FileEntry{}) {
		t.Fatal("nil and empty must match")
	}
	// A single field change flips the hash.
	c := []FileEntry{{Key: "a.cfg", Size: 1, SHA256: "aaa", Mode: 0o644}}
	d := []FileEntry{{Key: "a.cfg", Size: 1, SHA256: "aaa", Mode: 0o600}}
	if ContentHash(c) == ContentHash(d) {
		t.Fatal("mode must be part of the hash")
	}
}

func TestDecideSync(t *testing.T) {
	cases := []struct {
		name       string
		baseSeq    int32
		clientHash string
		headSeq    int32
		headHash   string
		want       SyncDecision
	}{
		{"first push, no head", 0, "x", 0, "", Proceed},
		{"identical content is noop", 0, "same", 5, "same", NoOp},
		{"based on head proceeds", 5, "new", 5, "old", Proceed},
		{"stale base different content conflicts", 4, "new", 5, "old", Conflict},
		{"stale base but same content is noop", 4, "same", 5, "same", NoOp},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DecideSync(tc.baseSeq, tc.clientHash, tc.headSeq, tc.headHash)
			if got != tc.want {
				t.Fatalf("DecideSync(%d,%q,%d,%q) = %s, want %s", tc.baseSeq, tc.clientHash, tc.headSeq, tc.headHash, got, tc.want)
			}
		})
	}
}
