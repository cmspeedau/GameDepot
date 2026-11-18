package ubisoft

import (
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	"github.com/cmspeedau/gamedepot/internal/proto/downloadpb"
	"github.com/cmspeedau/gamedepot/internal/store"
)

// slicePathAlphabet is the 32-character directory alphabet Ubisoft uses between
// "slices_v3/" and the slice hash.
const slicePathAlphabet = "0123456789abcdefghijklmnopqrstuv"

// hex40 is a SHA-1 written as hex, in either case; the plan normalises to upper.
var hex40 = regexp.MustCompile(`^[0-9A-Fa-f]{40}$`)

// PlanSlice is one slice to fetch and where its bytes belong in the assembled file.
type PlanSlice struct {
	// CDNPath is the relative path handed to the download service for URL minting.
	CDNPath string
	// VerifySha1 is the upper-case SHA-1 the decompressed slice body must hash to.
	VerifySha1 string
	// Offset is where this slice's content starts inside the file.
	Offset uint64
	// Size is the expected decompressed length.
	Size uint64
}

// PlanFile is one file to assemble.
type PlanFile struct {
	Path   string
	Size   uint64
	Slices []PlanSlice
}

// Plan is the flattened download work for one build.
type Plan struct {
	Files       []PlanFile
	TotalBytes  uint64
	TotalSlices int
}

// UniqueSlices returns each distinct slice once, in first-seen order.
//
// Ubisoft shares slices between files deliberately (the same library in several builds,
// the same asset in a patch), so fetching per file would download a title several times
// over. This is also the natural unit for the object cache: a slice is content-addressed
// by SHA-1, exactly like a depot object.
func (p Plan) UniqueSlices() []PlanSlice {
	seen := make(map[string]bool, p.TotalSlices)
	out := make([]PlanSlice, 0, p.TotalSlices)
	for _, f := range p.Files {
		for _, s := range f.Slices {
			if seen[s.CDNPath] {
				continue
			}
			seen[s.CDNPath] = true
			out = append(out, s)
		}
	}
	return out
}

// FileHashToPathChar derives the CDN directory for a slice hash.
//
// The rule is empirical, not documented by Ubisoft: take the first two hex digits,
// swap them, read the result as a byte, then index the 32-character alphabet at
// byte/16 plus 16 when the byte is odd. Ported as-is because a plausible-looking
// variation produces a path that 404s for every slice.
func FileHashToPathChar(hashHex string) (byte, error) {
	if len(hashHex) < 2 || !isHexByte(hashHex[:2]) {
		return 0, store.Configf("ubisoft: slice hash %q is not a hex SHA-1, so no CDN path can be derived",
			truncate(hashHex, 48))
	}
	swapped, err := hex.DecodeString(string([]byte{hashHex[1], hashHex[0]}))
	if err != nil {
		return 0, store.Configf("ubisoft: slice hash %q is not decodable", truncate(hashHex, 48))
	}
	b := swapped[0]
	index := int(b / 16)
	if b%2 != 0 {
		index += 16
	}
	return slicePathAlphabet[index], nil
}

func isHexByte(s string) bool {
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		case c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// SliceCdnPath is the CDN-relative path for one slice.
func SliceCdnPath(hashHexUpper string) (string, error) {
	dir, err := FileHashToPathChar(hashHexUpper)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("slices_v3/%c/%s", dir, strings.ToUpper(hashHexUpper)), nil
}

// SliceCdnPathCandidates returns the computed path first, then every other directory in
// the alphabet.
//
// The derivation is not published anywhere, so a title that turns out to use a different
// rule would otherwise fail outright: the streamer mints URLs for the remaining
// candidates rather than abandoning the ingest.
func SliceCdnPathCandidates(hashHexUpper string) ([]string, error) {
	hash := strings.ToUpper(hashHexUpper)
	computed, err := SliceCdnPath(hash)
	if err != nil {
		return nil, err
	}
	out := []string{computed}
	for _, c := range slicePathAlphabet {
		path := fmt.Sprintf("slices_v3/%c/%s", c, hash)
		if path != computed {
			out = append(out, path)
		}
	}
	return out, nil
}

// BuildPlan flattens a manifest into the files to assemble and the slices to fetch.
//
// Selection keeps every required chunk plus chunks tagged with the wanted language: a
// build's audio and video are split per language, so ignoring the tag would ship every
// voice pack in the title, and ignoring required chunks would ship none of the game.
//
// Manifest-declared paths are untrusted input. Backslashes normalise, and anything
// absolute or climbing out is refused here rather than at write time, so no path from a
// manifest reaches the filesystem unchecked.
func BuildPlan(m *downloadpb.Manifest, language string) (Plan, error) {
	if language == "" {
		language = "en-US"
	}
	wanted := strings.ToLower(language)

	var plan Plan
	for _, chunk := range m.GetChunks() {
		required := chunk.GetType() == downloadpb.Chunk_ChunkType_Required
		tagged := chunk.GetLanguage() != "" && strings.ToLower(chunk.GetLanguage()) == wanted
		if !required && !tagged {
			continue
		}
		for _, file := range chunk.GetFiles() {
			if file.GetIsDir() {
				continue // directories are implied by file paths
			}
			path, err := safePlanPath(file.GetName())
			if err != nil {
				return Plan{}, err
			}
			slices, err := planSlices(path, file)
			if err != nil {
				return Plan{}, err
			}
			pf := PlanFile{Path: path, Size: file.GetSize(), Slices: slices}
			plan.Files = append(plan.Files, pf)
			plan.TotalBytes += pf.Size
			plan.TotalSlices += len(slices)
		}
	}
	return plan, nil
}

// safePlanPath normalises a manifest path and rejects anything that could escape the
// version directory. Absolute paths are refused rather than made relative: a manifest
// that names /etc/passwd is either corrupt or hostile, and neither should be quietly
// rewritten into a location the operator did not choose.
func safePlanPath(name string) (string, error) {
	clean := strings.ReplaceAll(name, "\\", "/")
	if clean == "" {
		return "", store.Configf("ubisoft: manifest declares an empty file path")
	}
	if strings.HasPrefix(clean, "/") {
		return "", fmt.Errorf("ubisoft: manifest declares an absolute file path: %s", name)
	}
	if strings.Contains(clean, ":") {
		return "", fmt.Errorf("ubisoft: manifest declares an unsafe file path: %s", name)
	}
	for _, seg := range strings.Split(clean, "/") {
		if seg == "" {
			return "", fmt.Errorf("ubisoft: manifest declares a malformed file path: %s", name)
		}
		if seg == ".." {
			return "", fmt.Errorf("ubisoft: manifest declares a traversal path: %s", name)
		}
	}
	return clean, nil
}

// planSlices reconciles the two parallel arrays a manifest file carries: `slices`, the
// content SHA-1s, and `sliceList`, the per-slice descriptors giving size, an optional
// different download hash, and an offset.
//
// Three live behaviours are encoded here, each because a manifest was seen doing it:
// empty files with no slices at all (kept, because the game expects the path), offsets
// that are all zero meaning "sequential" rather than "everything at byte 0", and slices
// whose download hash differs from their content hash (a patched slice delivered under a
// different identity).
func planSlices(path string, file *downloadpb.File) ([]PlanSlice, error) {
	content := make([]string, 0, len(file.GetSlices()))
	for _, raw := range file.GetSlices() {
		content = append(content, strings.ToUpper(hex.EncodeToString(raw)))
	}
	descriptors := file.GetSliceList()

	switch {
	case len(descriptors) == 0 && len(content) == 0:
		// Empty placeholder files (dump.txt, placeholder.txt) are real entries in
		// live manifests; a file claiming bytes with no slices cannot be built.
		if file.GetSize() == 0 {
			return nil, nil
		}
		return nil, fmt.Errorf("ubisoft: manifest file %s declares %d bytes but carries no slices",
			path, file.GetSize())

	case len(descriptors) == 0:
		// A single slice with no descriptors is the simplest generation; anything
		// more than one cannot be placed without knowing its size.
		if len(content) != 1 {
			return nil, fmt.Errorf("ubisoft: manifest file %s has %d slice hashes but no slice descriptors",
				path, len(content))
		}
		if !hex40.MatchString(content[0]) {
			return nil, fmt.Errorf("ubisoft: manifest file %s carries a slice with no content hash; unsupported manifest generation",
				path)
		}
		cdnPath, err := SliceCdnPath(content[0])
		if err != nil {
			return nil, err
		}
		return []PlanSlice{{CDNPath: cdnPath, VerifySha1: content[0], Offset: 0, Size: file.GetSize()}}, nil

	case len(descriptors) != len(content):
		return nil, fmt.Errorf("ubisoft: manifest file %s declares %d slice descriptors for %d content hashes",
			path, len(descriptors), len(content))
	}

	// All-zero offsets are this generation's way of saying "in order", not
	// "every slice starts at 0".
	explicitOffset := false
	for _, d := range descriptors {
		if d.GetFileOffset() != 0 {
			explicitOffset = true
			break
		}
	}

	out := make([]PlanSlice, 0, len(descriptors))
	var cursor uint64
	for i, d := range descriptors {
		offset := cursor
		if explicitOffset {
			offset = d.GetFileOffset()
		}
		// An absent downloadSha1 means "same as the content hash".
		download := strings.ToUpper(hex.EncodeToString(d.GetDownloadSha1()))
		if !hex40.MatchString(download) {
			download = content[i]
		}
		if !hex40.MatchString(download) {
			return nil, fmt.Errorf("ubisoft: manifest file %s slice %d has no download or content hash; unsupported manifest generation",
				path, i)
		}
		if !hex40.MatchString(content[i]) {
			return nil, fmt.Errorf("ubisoft: manifest file %s slice %d declares no content hash to verify against; unsupported manifest generation",
				path, i)
		}
		cdnPath, err := SliceCdnPath(download)
		if err != nil {
			return nil, err
		}
		size := uint64(d.GetSize())
		out = append(out, PlanSlice{CDNPath: cdnPath, VerifySha1: content[i], Offset: offset, Size: size})
		cursor = offset + size
	}
	return out, nil
}
