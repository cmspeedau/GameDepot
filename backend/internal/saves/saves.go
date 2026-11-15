// Package saves holds the database-independent rules for cloud saves: the content
// hash that identifies a save set, and the sync/conflict decision. The server handlers
// and the desktop collector both rely on these, and they are tested without a database.
package saves

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
)

// FileEntry is one save file as described by the client: a logical key (path relative
// to the save root), size, plaintext hash and permission mode. The archive layout does
// not affect the content hash; only the file set does, so identical save sets dedupe.
type FileEntry struct {
	Key    string `json:"key"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Mode   uint32 `json:"mode"`
}

// ContentHash is a stable identity for a set of files. Entries are sorted by key, and
// the fields of each entry are marshalled in a fixed order, so the hash is independent
// of map iteration or tar ordering.
func ContentHash(entries []FileEntry) string {
	sorted := make([]FileEntry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Key < sorted[j].Key })

	h := sha256.New()
	for _, e := range sorted {
		// Fixed field order keeps the digest reproducible across languages: this is the
		// same contract the desktop applies (see desktop/src-tauri/src/saves.rs).
		line := fmt.Sprintf("%s\x00%d\x00%s\x00%o\n", e.Key, e.Size, e.SHA256, e.Mode&0o7777)
		h.Write([]byte(line))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// SyncDecision is what the server tells a client that wants to push.
type SyncDecision int

const (
	// Proceed: write a new version (first push, or client is based on current head).
	Proceed SyncDecision = iota
	// NoOp: the client's content already equals the head; nothing to store.
	NoOp
	// Conflict: the head advanced past the client's base and content differs; both sides
	// have changes. The client must pull/merge or push with force.
	Conflict
)

func (d SyncDecision) String() string {
	switch d {
	case Proceed:
		return "proceed"
	case NoOp:
		return "noop"
	case Conflict:
		return "conflict"
	}
	return "unknown"
}

// DecideSync compares the client's base version sequence and content hash against the
// current head.
//
//   - headSeq 0 means no head yet: always Proceed.
//   - Same content hash as head: NoOp (idempotent), regardless of base.
//   - Client based on the current head: Proceed.
//   - Client based on a stale head with different content: Conflict.
func DecideSync(clientBaseSeq int32, clientContentHash string, headSeq int32, headContentHash string) SyncDecision {
	if clientContentHash != "" && clientContentHash == headContentHash {
		return NoOp
	}
	if headSeq == 0 {
		return Proceed
	}
	if clientBaseSeq == headSeq {
		return Proceed
	}
	return Conflict
}
