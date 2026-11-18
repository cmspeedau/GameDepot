// Package ubisoft is the Ubisoft Connect storefront adapter.
//
// Two things distinguish this provider from GOG. Content is addressed by SHA-1 slices
// that several files can share, which is close to this project's own object store, and
// the install manifest is a protobuf document wrapped in a fixed-size header with no
// framing announcement. Both are handled here, in pure code with no network calls: the
// socket client that talks to the demux service lives in socket.go.
//
// The wire facts encoded here were worked out live in this project's archived
// predecessor and are reimplemented in Go per ADR-0009 and ADR-0011.
package ubisoft

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"errors"
	"fmt"
	"io"

	"github.com/klauspost/compress/zstd"
	"google.golang.org/protobuf/proto"

	"github.com/cmspeedau/gamedepot/internal/proto/downloadpb"
)

// manifestHeaderBytes is the unlabelled prefix in front of every install manifest:
// version bytes followed by a base64 signature. It carries no length field, so the
// payload always starts at a fixed offset. Live-verified against a real title.
const manifestHeaderBytes = 356

// maxDecodedManifest bounds what one manifest may inflate to. A manifest is a few
// megabytes in practice; the limit exists so a hostile or corrupt blob cannot make the
// ingest allocate without bound.
const maxDecodedManifest = 512 << 20

// ErrManifestUndecodable means no known framing produced a readable game manifest.
var ErrManifestUndecodable = errors.New("ubisoft: install manifest did not decode as a game manifest")

// ParseManifest turns a downloaded manifest blob into a game manifest.
//
// Ubisoft serves the payload under at least three framings and says nothing about which
// one it used, so the magic bytes decide the order and the rest are tried blind:
// zstd (modern titles), gzip, zlib, then raw deflate, and finally the payload as-is.
// protobuf refuses almost nothing, so a candidate only wins if it yields a manifest that
// actually carries files; otherwise the next framing is tried. That check is what stops
// a wrongly-inflated blob from decoding into an empty manifest that would publish an
// empty release.
func ParseManifest(blob []byte) (*downloadpb.Manifest, error) {
	if len(blob) <= manifestHeaderBytes {
		return nil, fmt.Errorf("ubisoft: install manifest is truncated (%d bytes, header alone is %d)",
			len(blob), manifestHeaderBytes)
	}
	body := blob[manifestHeaderBytes:]

	order := make([]framing, 0, 4)
	switch {
	case len(body) >= 2 && body[0] == 0x28 && body[1] == 0xB5:
		order = append(order, framing{"zstd", decompressZstd})
	case len(body) >= 2 && body[0] == 0x1F && body[1] == 0x8B:
		order = append(order, framing{"gzip", decompressGzip})
	case len(body) >= 1 && body[0] == 0x78:
		order = append(order, framing{"zlib", decompressZlib})
	}
	// Blind fallbacks, in the order the predecessor's probe found useful. Some legacy
	// manifests arrive raw-deflated with no magic at all.
	for _, f := range []framing{
		{"raw-deflate", decompressFlate},
		{"zstd", decompressZstd},
		{"stored", passThrough},
	} {
		if !containsName(order, f.name) {
			order = append(order, f)
		}
	}

	for _, f := range order {
		payload, err := f.unwrap(body)
		if err != nil {
			continue
		}
		var m downloadpb.Manifest
		if err := proto.Unmarshal(payload, &m); err != nil {
			continue
		}
		if manifestCarriesFiles(&m) {
			return &m, nil
		}
	}
	return nil, ErrManifestUndecodable
}

// framing names a compression candidate so the magic-byte branch and the blind
// fallbacks cannot both try the same one.
type framing struct {
	name   string
	unwrap func([]byte) ([]byte, error)
}

func containsName(fs []framing, name string) bool {
	for _, f := range fs {
		if f.name == name {
			return true
		}
	}
	return false
}

// manifestCarriesFiles reports whether any chunk lists files. A manifest with chunks but
// no files anywhere is a framing miss, not an empty game.
func manifestCarriesFiles(m *downloadpb.Manifest) bool {
	for _, chunk := range m.GetChunks() {
		if len(chunk.GetFiles()) > 0 {
			return true
		}
	}
	return false
}

func decompressZstd(body []byte) ([]byte, error) {
	dec, err := zstd.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer dec.Close()
	return readCapped(dec)
}

func decompressGzip(body []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return readCapped(r)
}

func decompressZlib(body []byte) ([]byte, error) {
	r, err := zlib.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return readCapped(r)
}

func decompressFlate(body []byte) ([]byte, error) {
	r := flate.NewReader(bytes.NewReader(body))
	defer r.Close()
	return readCapped(r)
}

func passThrough(body []byte) ([]byte, error) { return readCapped(bytes.NewReader(body)) }

func readCapped(r io.Reader) ([]byte, error) {
	out, err := io.ReadAll(io.LimitReader(r, maxDecodedManifest+1))
	if err != nil {
		return nil, err
	}
	if len(out) > maxDecodedManifest {
		return nil, fmt.Errorf("ubisoft: manifest payload inflates past %d bytes", maxDecodedManifest)
	}
	return out, nil
}
