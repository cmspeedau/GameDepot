package epic

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"testing"
)

// ---------- manifest builder (test fixture) ----------

type epicBuf struct {
	b []byte
}

func (b *epicBuf) u8(v uint8)    { b.b = append(b.b, v) }
func (b *epicBuf) u32(v uint32)  { b.b = append(b.b, byte(v), byte(v>>8), byte(v>>16), byte(v>>24)) }
func (b *epicBuf) i32(v int32)   { b.u32(uint32(v)) }
func (b *epicBuf) raw(bs []byte) { b.b = append(b.b, bs...) }
func (b *epicBuf) fstr(s string) {
	if s == "" {
		b.i32(0)
		return
	}
	b.i32(int32(len(s) + 1))
	b.raw([]byte(s))
	b.u8(0)
}

func zlibCompress(b []byte) []byte {
	var buf bytes.Buffer
	w, err := zlib.NewWriterLevel(&buf, zlib.DefaultCompression)
	if err != nil {
		panic(err)
	}
	if _, err := w.Write(b); err != nil {
		panic(err)
	}
	if err := w.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// epicManifest builds one compressed Epic binary manifest out of metadata and files. Every
// body section is written as a size field followed by its content, which is the layout the
// decoder walks.
func epicManifest(m EpicManifestMeta, files []EpicManifestFile) []byte {
	meta := &epicBuf{}
	meta.u8(0)   // dataVersion
	meta.u32(17) // featureLevel
	meta.u8(0)   // isFileData
	meta.u32(0)  // appID
	meta.fstr(m.AppName)
	meta.fstr(m.BuildVersion)
	meta.fstr(m.LaunchExe)
	meta.fstr(m.LaunchCommand)
	meta.u32(0)   // prereq count
	meta.fstr("") // prereq name
	meta.fstr("") // prereq path
	meta.fstr("") // prereq args

	cdl := &epicBuf{}
	cdl.u8(0)                 // version
	cdl.u32(1)                // chunk count
	cdl.raw(make([]byte, 16)) // guid
	cdl.raw(make([]byte, 8))  // hash
	cdl.raw(make([]byte, 20)) // sha1
	cdl.u8(0)                 // group
	cdl.u32(0)                // window
	cdl.i32(0)                // file size

	fml := &epicBuf{}
	fml.u8(0) // version
	fml.u32(uint32(len(files)))
	for i := range files {
		fml.fstr(files[i].Path)
		fml.fstr(files[i].Symlink)
		fml.raw(make([]byte, 20)) // sha1
		fml.u8(files[i].Flags)
		fml.u32(0) // tag count
		fml.u32(uint32(len(files[i].ChunkParts)))
		for j := range files[i].ChunkParts {
			part := files[i].ChunkParts[j]
			fml.u32(28) // entry size
			guid := make([]byte, 16)
			binary.LittleEndian.PutUint32(guid[0:4], part.Guid[0])
			binary.LittleEndian.PutUint32(guid[4:8], part.Guid[1])
			binary.LittleEndian.PutUint32(guid[8:12], part.Guid[2])
			binary.LittleEndian.PutUint32(guid[12:16], part.Guid[3])
			fml.raw(guid)
			fml.u32(uint32(part.Offset))
			fml.u32(uint32(part.Size))
		}
	}

	custom := &epicBuf{}
	custom.u8(0)  // version
	custom.u32(0) // count

	body := &epicBuf{}
	body.u32(uint32(4 + len(meta.b)))
	body.raw(meta.b)
	body.u32(uint32(4 + len(cdl.b)))
	body.raw(cdl.b)
	body.u32(uint32(4 + len(fml.b)))
	body.raw(fml.b)
	body.u32(uint32(4 + len(custom.b)))
	body.raw(custom.b)

	compressed := zlibCompress(body.b)
	sum := sha1.Sum(body.b)
	out := &epicBuf{}
	out.u32(0x44BEC00C)
	out.u32(41)
	out.u32(uint32(len(body.b)))     // sizeUncompressed
	out.u32(uint32(len(compressed))) // sizeCompressed
	out.raw(sum[:])
	out.u8(1)   // compressed
	out.u32(17) // version
	out.raw(compressed)
	return out.b
}

// ---------- header ----------

func TestParseEpicManifestRejectsBadMagic(t *testing.T) {
	blob := epicManifest(EpicManifestMeta{}, nil)
	blob[0] = 0x0d
	if _, err := ParseEpicBinaryManifest(blob); !errors.Is(err, ErrManifestMagic) {
		t.Fatalf("err = %v, want ErrManifestMagic", err)
	}
}

func TestParseEpicManifestRejectsUnsupportedVersion(t *testing.T) {
	blob := epicManifest(EpicManifestMeta{}, nil)
	binary.LittleEndian.PutUint32(blob[37:41], 25)
	if _, err := ParseEpicBinaryManifest(blob); !errors.Is(err, ErrManifestVersion) {
		t.Fatalf("err = %v, want ErrManifestVersion", err)
	}
}

func TestParseEpicManifestRejectsChecksumMismatch(t *testing.T) {
	blob := epicManifest(EpicManifestMeta{}, nil)
	blob[20] ^= 0xff
	if _, err := ParseEpicBinaryManifest(blob); !errors.Is(err, ErrManifestChecksum) {
		t.Fatalf("err = %v, want ErrManifestChecksum", err)
	}
}

func TestParseEpicManifestRejectsTruncated(t *testing.T) {
	blob := epicManifest(EpicManifestMeta{}, nil)
	if _, err := ParseEpicBinaryManifest(blob[:10]); !errors.Is(err, ErrManifestTruncated) {
		t.Fatalf("err = %v, want ErrManifestTruncated", err)
	}
}

func TestParseEpicManifestRejectsBadHeaderSize(t *testing.T) {
	blob := epicManifest(EpicManifestMeta{}, nil)
	binary.LittleEndian.PutUint32(blob[4:8], 40)
	if _, err := ParseEpicBinaryManifest(blob); !errors.Is(err, ErrManifestStructure) {
		t.Fatalf("err = %v, want ErrManifestStructure", err)
	}
}

// ---------- happy path ----------

func TestParseEpicManifestDecodesMetadataAndFiles(t *testing.T) {
	meta := EpicManifestMeta{
		DataVersion:   0,
		FeatureLevel:  17,
		AppName:       "Hitman",
		BuildVersion:  "4226454",
		LaunchExe:     "Retail/HITMAN.exe",
		LaunchCommand: "",
	}
	files := []EpicManifestFile{{
		Path:       "Retail/HITMAN.exe",
		Symlink:    "",
		Sha1:       hexSHA1(make([]byte, 20)),
		Flags:      4,
		ChunkParts: []EpicManifestChunkPart{{Guid: [4]uint32{0xd226bbce, 0x40bd441d, 0x7d37c1b0, 0x5b087b93}, Offset: 0, Size: 32369656, FileOffset: 0}},
		FileSize:   32369656,
	}}
	parsed, err := ParseEpicBinaryManifest(epicManifest(meta, files))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Meta.AppName != "Hitman" || parsed.Meta.BuildVersion != "4226454" {
		t.Fatalf("meta = %+v", parsed.Meta)
	}
	if parsed.Meta.LaunchExe != "Retail/HITMAN.exe" {
		t.Fatalf("launch exe = %q", parsed.Meta.LaunchExe)
	}
	if len(parsed.Files) != 1 {
		t.Fatalf("files = %d, want 1", len(parsed.Files))
	}
	f := parsed.Files[0]
	if f.Path != "Retail/HITMAN.exe" || f.Symlink != "" || f.Flags != 4 {
		t.Fatalf("file = %+v", f)
	}
	if f.Sha1 != hexSHA1(make([]byte, 20)) || f.FileSize != 32369656 {
		t.Fatalf("file hashes = %q / %d", f.Sha1, f.FileSize)
	}
	if len(f.ChunkParts) != 1 || f.ChunkParts[0].Size != 32369656 || f.ChunkParts[0].FileOffset != 0 {
		t.Fatalf("chunk parts = %+v", f.ChunkParts)
	}
}
