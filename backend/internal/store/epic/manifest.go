// Package epic is the Epic Games Store adapter.
//
// Two things distinguish this provider from GOG. Content is addressed by opaque chunk
// GUIDs that several files can share, and the install manifest is a proprietary binary
// document that must be decoded before anything can be planned.
//
// The wire facts encoded here were worked out live in this project's archived predecessor
// (ADR-0009) and are reimplemented in Go. The binary layout is reverse engineered rather
// than officially specified: Epic publishes no format document, so every field below was
// confirmed against a real build manifest before it was trusted.

package epic

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"unicode/utf16"
)

// epicManifestMagic is the magic at offset 0 of every Epic binary build manifest.
const epicManifestMagic uint32 = 0x44BEC00C

// epicManifestHeaderSize is the header size before the compressed body. Manifests at or
// below serialisation version 21 carry exactly this many bytes; later versions append the
// secret GUID and encryption tag the launcher adds for protected builds.
const epicManifestHeaderSize uint32 = 41

// epicManifestMaxBytes bounds one manifest download. Real manifests are a few megabytes;
// the cap exists so a hostile or corrupt response cannot make the ingest buffer without
// bound.
const epicManifestMaxBytes int64 = 64 << 20

// Epic manifest storage flags, from the launcher's own flag byte.
const (
	epicStorageUncompressed uint8 = 0
	epicStorageCompressed   uint8 = 1
	epicStorageEncrypted    uint8 = 2
)

// epicMaxSerialisationVersion is the newest manifest serialisation this decoder knows.
// Beyond it the launcher adds fields this project has never seen a sample of.
const epicMaxSerialisationVersion uint32 = 24

// Epic manifest file flags; the only one this project honours is the unix executable bit.
const epicUnixExecutable uint8 = 0x4

// Decoder errors. They are sentinel values so callers can branch on the failure without
// matching messages, and wrapping keeps the sentinel available to errors.Is.
var (
	ErrManifestMagic     = errors.New("epic: manifest magic mismatch")
	ErrManifestVersion   = errors.New("epic: unsupported epic manifest version")
	ErrManifestChecksum  = errors.New("epic: epic manifest checksum mismatch")
	ErrManifestTruncated = errors.New("epic: epic manifest is truncated")
	ErrManifestStructure = errors.New("epic: epic manifest body structure is inconsistent")
	ErrManifestEncrypted = errors.New("epic: encrypted epic manifests are not supported")
)

// EpicManifestMeta is the install metadata one decoded manifest carries.
type EpicManifestMeta struct {
	DataVersion   int32
	FeatureLevel  int32
	IsFileData    bool
	AppID         uint32
	AppName       string
	BuildVersion  string
	LaunchExe     string
	LaunchCommand string
	PrereqIDs     []string
}

// EpicManifestChunkPart is one extent of a file inside the chunk stream.
type EpicManifestChunkPart struct {
	// Guid is Epic's opaque 128-bit chunk identifier, four little-endian words.
	Guid [4]uint32
	// Offset is the chunk-stream position this part starts at.
	Offset uint32
	// Size is the number of bytes this part contributes to the file.
	Size uint32
	// FileOffset is where the part lands inside the assembled file.
	FileOffset uint64
}

// EpicManifestFile is one file the build installs, with the chunks that carry it.
type EpicManifestFile struct {
	Path        string
	Symlink     string
	Sha1        string
	Flags       uint8
	InstallTags []string
	ChunkParts  []EpicManifestChunkPart
	// FileSize is the assembled size; it is the sum of the part sizes when the stream is
	// contiguous, which is how Epic's own writer lays files down.
	FileSize int64
}

// EpicBinaryManifest is one decoded Epic build manifest.
type EpicBinaryManifest struct {
	Meta  EpicManifestMeta
	Files []EpicManifestFile
}

// epicCursor walks a byte buffer with bounds checks. Every read is an error rather than a
// panic, because a manifest that runs past its declared size must fail loudly.
type epicCursor struct {
	data []byte
	pos  int
}

func newEpicCursor(data []byte) *epicCursor { return &epicCursor{data: data} }

func (c *epicCursor) seek(pos int) error {
	if pos < 0 || pos > len(c.data) {
		return ErrManifestTruncated
	}
	c.pos = pos
	return nil
}

func (c *epicCursor) read(n int) ([]byte, error) {
	if n < 0 || c.pos+n > len(c.data) {
		return nil, ErrManifestTruncated
	}
	b := c.data[c.pos : c.pos+n]
	c.pos += n
	return b, nil
}

func (c *epicCursor) u8() (uint8, error) {
	b, err := c.read(1)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}

func (c *epicCursor) u32() (uint32, error) {
	b, err := c.read(4)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b), nil
}

func (c *epicCursor) u64() (uint64, error) {
	b, err := c.read(8)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(b), nil
}

func (c *epicCursor) skip(n int) error {
	if n < 0 || c.pos+n > len(c.data) {
		return ErrManifestTruncated
	}
	c.pos += n
	return nil
}

func (c *epicCursor) i32() (int32, error) { v, err := c.u32(); return int32(v), err }

// fstring reads one Epic length-prefixed string: a signed 32-bit byte length whose sign
// selects ASCII (positive) or UTF-16LE (negative), followed by the bytes and the NUL
// terminator the ASCII form carries.
func (c *epicCursor) fstring() (string, error) {
	ln, err := c.i32()
	if err != nil {
		return "", err
	}
	if ln < 0 {
		n := int64(-2 * int64(ln))
		b, err := c.read(int(n) - 2)
		if err != nil {
			return "", err
		}
		if err := c.skip(2); err != nil { // two byte UTF-16 terminator
			return "", err
		}
		units := make([]uint16, 0, len(b)/2)
		for i := 0; i+1 < len(b); i += 2 {
			units = append(units, uint16(b[i])|uint16(b[i+1])<<8)
		}
		return string(utf16.Decode(units)), nil
	}
	if ln == 0 {
		return "", nil
	}
	b, err := c.read(int(ln) - 1)
	if err != nil {
		return "", err
	}
	if err := c.skip(1); err != nil { // string terminator
		return "", err
	}
	return string(b), nil
}

// ParseEpicBinaryManifest decodes an Epic binary build manifest.
//
// The container is a small fixed header, an optional zlib stream, and a body made of
// metadata, chunk-data-list, file-manifest-list and custom-fields sections. The sections
// are length prefixed, so each one can be located without parsing the ones before it.
//
// Any structural failure is reported through the sentinels; callers treat those as provider
// refusals rather than retrying with a different framing.
func ParseEpicBinaryManifest(blob []byte) (*EpicBinaryManifest, error) {
	c := newEpicCursor(blob)
	magic, err := c.u32()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrManifestTruncated, err)
	}
	if magic != epicManifestMagic {
		return nil, fmt.Errorf("%w: 0x%08x", ErrManifestMagic, magic)
	}
	headerSize, err := c.u32()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrManifestTruncated, err)
	}
	sizeUncompressed, err := c.u32()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrManifestTruncated, err)
	}
	sizeCompressed, err := c.u32()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrManifestTruncated, err)
	}
	checksum := make([]byte, 20)
	if _, err := c.read(20); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrManifestTruncated, err)
	}
	copy(checksum, c.data[c.pos-20:c.pos])
	storedAs, err := c.u8()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrManifestTruncated, err)
	}
	version, err := c.u32()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrManifestTruncated, err)
	}
	if version > epicMaxSerialisationVersion {
		return nil, fmt.Errorf("%w: version %d", ErrManifestVersion, version)
	}
	if version >= 22 {
		if _, err := c.read(16); err != nil { // secret GUID
			return nil, fmt.Errorf("%w: %w", ErrManifestTruncated, err)
		}
		if _, err := c.read(16); err != nil { // encryption tag
			return nil, fmt.Errorf("%w: %w", ErrManifestTruncated, err)
		}
		if storedAs&epicStorageEncrypted != 0 {
			return nil, ErrManifestEncrypted
		}
	}
	if uint64(c.pos) != uint64(headerSize) {
		return nil, fmt.Errorf("%w: header is %d bytes, expected %d", ErrManifestStructure, c.pos, headerSize)
	}

	body := blob[c.pos:]
	if int64(len(body)) < int64(sizeCompressed) {
		return nil, ErrManifestTruncated
	}
	data := body[:sizeCompressed]
	if storedAs&epicStorageCompressed != 0 {
		dec, err := zlib.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrManifestTruncated, err)
		}
		out, err := io.ReadAll(io.LimitReader(dec, int64(sizeUncompressed)+1))
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrManifestTruncated, err)
		}
		if int64(len(out)) > int64(sizeUncompressed) {
			return nil, ErrManifestTruncated
		}
		sum := sha1.Sum(out)
		if !bytes.Equal(sum[:], checksum) {
			return nil, ErrManifestChecksum
		}
		data = out
	}

	m := &EpicBinaryManifest{}
	if err := parseEpicBody(data, m); err != nil {
		return nil, err
	}
	return m, nil
}

// parseEpicBody walks the metadata, chunk-data-list, file-manifest-list and custom-fields
// sections of a decompressed manifest, filling m.
func parseEpicBody(data []byte, m *EpicBinaryManifest) error {
	c := newEpicCursor(data)

	metaSize, err := c.u32()
	if err != nil {
		return fmt.Errorf("%w: metadata: %w", ErrManifestTruncated, err)
	}
	metaContentStart := c.pos
	metaEnd := metaContentStart + int(metaSize) - 4
	meta := newEpicCursor(data)
	if err := meta.seek(metaContentStart); err != nil {
		return fmt.Errorf("%w: metadata: %w", ErrManifestTruncated, err)
	}
	dataVersion, err := meta.u8()
	if err != nil {
		return fmt.Errorf("%w: metadata data version: %w", ErrManifestTruncated, err)
	}
	featureLevel, err := meta.u32()
	if err != nil {
		return fmt.Errorf("%w: metadata feature level: %w", ErrManifestTruncated, err)
	}
	isFileData, err := meta.u8()
	if err != nil {
		return fmt.Errorf("%w: metadata isFileData: %w", ErrManifestTruncated, err)
	}
	appID, err := meta.u32()
	if err != nil {
		return fmt.Errorf("%w: metadata app id: %w", ErrManifestTruncated, err)
	}
	appName, err := meta.fstring()
	if err != nil {
		return fmt.Errorf("%w: metadata app name: %w", ErrManifestTruncated, err)
	}
	buildVersion, err := meta.fstring()
	if err != nil {
		return fmt.Errorf("%w: metadata build version: %w", ErrManifestTruncated, err)
	}
	launchExe, err := meta.fstring()
	if err != nil {
		return fmt.Errorf("%w: metadata launch exe: %w", ErrManifestTruncated, err)
	}
	launchCommand, err := meta.fstring()
	if err != nil {
		return fmt.Errorf("%w: metadata launch command: %w", ErrManifestTruncated, err)
	}
	prereqCount, err := meta.u32()
	if err != nil {
		return fmt.Errorf("%w: metadata prereq count: %w", ErrManifestTruncated, err)
	}
	prereqIDs := make([]string, 0, prereqCount)
	for i := int64(0); int64(i) < int64(prereqCount); i++ {
		id, err := meta.fstring()
		if err != nil {
			return fmt.Errorf("%w: metadata prereq %d: %w", ErrManifestTruncated, i, err)
		}
		prereqIDs = append(prereqIDs, id)
	}
	if _, err = meta.fstring(); err != nil { // prereq name
		return fmt.Errorf("%w: metadata prereq name: %w", ErrManifestTruncated, err)
	}
	if _, err = meta.fstring(); err != nil { // prereq path
		return fmt.Errorf("%w: metadata prereq path: %w", ErrManifestTruncated, err)
	}
	if _, err = meta.fstring(); err != nil { // prereq args
		return fmt.Errorf("%w: metadata prereq args: %w", ErrManifestTruncated, err)
	}
	if dataVersion > 0 {
		if _, err = meta.fstring(); err != nil { // build id
			return fmt.Errorf("%w: metadata build id: %w", ErrManifestTruncated, err)
		}
	}
	if dataVersion > 1 {
		if _, err = meta.fstring(); err != nil { // uninstall action path
			return fmt.Errorf("%w: metadata uninstall path: %w", ErrManifestTruncated, err)
		}
		if _, err = meta.fstring(); err != nil { // uninstall action args
			return fmt.Errorf("%w: metadata uninstall args: %w", ErrManifestTruncated, err)
		}
	}
	if meta.pos != metaEnd {
		return fmt.Errorf("%w: metadata spans %d bytes, declared %d", ErrManifestStructure, metaEnd-metaContentStart, metaSize)
	}
	m.Meta = EpicManifestMeta{
		DataVersion:   int32(dataVersion),
		FeatureLevel:  int32(featureLevel),
		IsFileData:    isFileData != 0,
		AppID:         appID,
		AppName:       appName,
		BuildVersion:  buildVersion,
		LaunchExe:     launchExe,
		LaunchCommand: launchCommand,
		PrereqIDs:     prereqIDs,
	}

	c.seek(metaEnd)
	cdlSize, err := c.u32()
	if err != nil {
		return fmt.Errorf("%w: chunk data list: %w", ErrManifestTruncated, err)
	}
	cdlEnd := c.pos + int(cdlSize) - 4
	cdl := newEpicCursor(data)
	if err := cdl.seek(c.pos); err != nil { // cdl content starts after its size field
		return fmt.Errorf("%w: chunk data list: %w", ErrManifestTruncated, err)
	}
	cdlVersion, err := cdl.u8()
	if err != nil {
		return fmt.Errorf("%w: chunk data list version: %w", ErrManifestTruncated, err)
	}
	chunkCount, err := cdl.u32()
	if err != nil {
		return fmt.Errorf("%w: chunk data list count: %w", ErrManifestTruncated, err)
	}
	if err := skipEpicChunks(cdl, chunkCount, int64(cdlVersion) >= 22); err != nil {
		return fmt.Errorf("%w: chunk data list: %w", ErrManifestTruncated, err)
	}
	if cdl.pos != cdlEnd {
		return fmt.Errorf("%w: chunk data list spans %d bytes, declared %d", ErrManifestStructure, cdlEnd-cdl.pos, cdlSize)
	}
	c.seek(cdlEnd)

	fmlSize, err := c.u32()
	if err != nil {
		return fmt.Errorf("%w: file manifest list: %w", ErrManifestTruncated, err)
	}
	fmlEnd := c.pos + int(fmlSize) - 4
	fml := newEpicCursor(data)
	if err := fml.seek(c.pos); err != nil {
		return fmt.Errorf("%w: file manifest list: %w", ErrManifestTruncated, err)
	}
	if err := parseEpicFiles(fml, m, fmlEnd); err != nil {
		return err
	}
	if fml.pos != fmlEnd {
		return fmt.Errorf("%w: file manifest list spans %d bytes, declared %d", ErrManifestStructure, fmlEnd-fml.pos, fmlSize)
	}

	c.seek(fmlEnd)
	customDataSize, err := c.u32()
	if err != nil {
		return fmt.Errorf("%w: custom fields: %w", ErrManifestTruncated, err)
	}
	if err := c.skip(int(customDataSize) - 4); err != nil {
		return fmt.Errorf("%w: custom fields: %w", ErrManifestTruncated, err)
	}
	if c.pos != fmlEnd+int(customDataSize) {
		return fmt.Errorf("%w: custom fields span %d bytes, declared %d", ErrManifestStructure, fmlEnd+int(customDataSize)-c.pos, customDataSize)
	}
	return nil
}

// skipEpicChunks consumes one chunk-data-list body: version, count and the per-chunk
// fields. The version-gated tail only exists from serialisation version 22 on.
func skipEpicChunks(c *epicCursor, count uint32, versioned bool) error {
	for i := uint32(0); i < count; i++ {
		if _, err := c.read(16); err != nil { // GUID
			return err
		}
		if _, err := c.read(8); err != nil { // hash
			return err
		}
		if _, err := c.read(20); err != nil { // SHA-1
			return err
		}
		if _, err := c.read(1); err != nil { // group number
			return err
		}
		if _, err := c.read(4); err != nil { // window size
			return err
		}
		if _, err := c.read(4); err != nil { // file size
			return err
		}
		if versioned {
			if _, err := c.read(16); err != nil { // secret GUID
				return err
			}
			if _, err := c.read(4); err != nil { // compressed window size
				return err
			}
			if _, err := c.read(16); err != nil { // encryption tag
				return err
			}
		}
	}
	return nil
}

// parseEpicFiles reads one file-manifest-list body into the manifest's file list.
func parseEpicFiles(f *epicCursor, m *EpicBinaryManifest, end int) error {
	version, err := f.u8()
	if err != nil {
		return fmt.Errorf("%w: file manifest list version: %w", ErrManifestTruncated, err)
	}
	count, err := f.u32()
	if err != nil {
		return fmt.Errorf("%w: file manifest list count: %w", ErrManifestTruncated, err)
	}
	if end > len(f.data) {
		return ErrManifestTruncated
	}
	names := make([]string, count)
	for i := uint32(0); i < count; i++ {
		n, err := f.fstring()
		if err != nil {
			return fmt.Errorf("%w: file %d name: %w", ErrManifestTruncated, i, err)
		}
		names[i] = n
	}
	symlinks := make([]string, count)
	for i := uint32(0); i < count; i++ {
		s, err := f.fstring()
		if err != nil {
			return fmt.Errorf("%w: file %d symlink: %w", ErrManifestTruncated, i, err)
		}
		symlinks[i] = s
	}
	hashes := make([]string, count)
	for i := uint32(0); i < count; i++ {
		b, err := f.read(20)
		if err != nil {
			return fmt.Errorf("%w: file %d hash: %w", ErrManifestTruncated, i, err)
		}
		hashes[i] = hexSHA1(b)
	}
	flags := make([]uint8, count)
	for i := uint32(0); i < count; i++ {
		v, err := f.u8()
		if err != nil {
			return fmt.Errorf("%w: file %d flags: %w", ErrManifestTruncated, i, err)
		}
		flags[i] = v
	}
	tags := make([][]string, count)
	for i := uint32(0); i < count; i++ {
		n, err := f.u32()
		if err != nil {
			return fmt.Errorf("%w: file %d tag count: %w", ErrManifestTruncated, i, err)
		}
		list := make([]string, 0, n)
		for j := uint32(0); j < n; j++ {
			tag, err := f.fstring()
			if err != nil {
				return fmt.Errorf("%w: file %d tag %d: %w", ErrManifestTruncated, i, j, err)
			}
			list = append(list, tag)
		}
		tags[i] = list
	}
	parts := make([][]EpicManifestChunkPart, count)
	fileSizes := make([]int64, count)
	for i := uint32(0); i < count; i++ {
		n, err := f.u32()
		if err != nil {
			return fmt.Errorf("%w: file %d part count: %w", ErrManifestTruncated, i, err)
		}
		list := make([]EpicManifestChunkPart, 0, n)
		var fileSize int64
		for j := uint32(0); j < n; j++ {
			entrySize, err := f.u32()
			if err != nil {
				return fmt.Errorf("%w: file %d part %d entry size: %w", ErrManifestTruncated, i, j, err)
			}
			part := EpicManifestChunkPart{FileOffset: uint64(fileSize)}
			part.Guid[0], err = f.u32()
			if err != nil {
				return fmt.Errorf("%w: file %d part %d guid: %w", ErrManifestTruncated, i, j, err)
			}
			part.Guid[1], err = f.u32()
			if err != nil {
				return fmt.Errorf("%w: file %d part %d guid: %w", ErrManifestTruncated, i, j, err)
			}
			part.Guid[2], err = f.u32()
			if err != nil {
				return fmt.Errorf("%w: file %d part %d guid: %w", ErrManifestTruncated, i, j, err)
			}
			part.Guid[3], err = f.u32()
			if err != nil {
				return fmt.Errorf("%w: file %d part %d guid: %w", ErrManifestTruncated, i, j, err)
			}
			part.Offset, err = f.u32()
			if err != nil {
				return fmt.Errorf("%w: file %d part %d offset: %w", ErrManifestTruncated, i, j, err)
			}
			part.Size, err = f.u32()
			if err != nil {
				return fmt.Errorf("%w: file %d part %d size: %w", ErrManifestTruncated, i, j, err)
			}
			if int64(entrySize) < 28 {
				return fmt.Errorf("%w: file %d part %d entry size %d is below the minimum", ErrManifestTruncated, i, j, entrySize)
			}
			if err := f.skip(int(entrySize) - 28); err != nil { // trailing bytes the format allows
				return fmt.Errorf("%w: file %d part %d: %w", ErrManifestTruncated, i, j, err)
			}
			list = append(list, part)
			fileSize += int64(part.Size)
		}
		parts[i] = list
		fileSizes[i] = fileSize
	}
	if version > 0 {
		for i := uint32(0); i < count; i++ {
			hasMD5, err := f.u32()
			if err != nil {
				return fmt.Errorf("%w: file %d md5 flag: %w", ErrManifestTruncated, i, err)
			}
			if hasMD5 != 0 {
				if _, err := f.read(16); err != nil { // MD5
					return fmt.Errorf("%w: file %d md5: %w", ErrManifestTruncated, i, err)
				}
			}
			if _, err := f.fstring(); err != nil { // MIME type
				return fmt.Errorf("%w: file %d mime: %w", ErrManifestTruncated, i, err)
			}
		}
	}
	if version > 1 {
		for i := uint32(0); i < count; i++ {
			if _, err := f.read(32); err != nil { // SHA-256
				return fmt.Errorf("%w: file %d sha256: %w", ErrManifestTruncated, i, err)
			}
		}
	}

	m.Files = make([]EpicManifestFile, 0, count)
	for i := uint32(0); i < count; i++ {
		m.Files = append(m.Files, EpicManifestFile{
			Path:        names[i],
			Symlink:     symlinks[i],
			Sha1:        hashes[i],
			Flags:       flags[i],
			InstallTags: tags[i],
			ChunkParts:  parts[i],
			FileSize:    fileSizes[i],
		})
	}
	return nil
}

func hexSHA1(b []byte) string {
	const hexDigits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, x := range b {
		out = append(out, hexDigits[x>>4], hexDigits[x&0xf])
	}
	return string(out)
}
