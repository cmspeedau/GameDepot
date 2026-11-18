package ubisoft

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/cmspeedau/gamedepot/internal/proto/downloadpb"
	"github.com/cmspeedau/gamedepot/internal/store"
)

// ---------- manifest fixtures ----------

func manifestBytes(t *testing.T, m *downloadpb.Manifest) []byte {
	t.Helper()
	raw, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func sampleManifest() *downloadpb.Manifest {
	sha := func(s string) []byte {
		b, _ := hex.DecodeString(s)
		return b
	}
	return &downloadpb.Manifest{
		Chunks: []*downloadpb.Chunk{
			{
				Id:   proto.Uint32(1),
				Type: downloadpb.Chunk_ChunkType_Required.Enum(),
				Files: []*downloadpb.File{{
					Name:   proto.String("game/bin.exe"),
					Size:   proto.Uint64(10),
					IsDir:  proto.Bool(false),
					Slices: [][]byte{sha(strings.Repeat("a", 40))},
				}},
			},
		},
	}
}

func withHeader(t *testing.T, payload []byte) []byte {
	t.Helper()
	head := make([]byte, manifestHeaderBytes)
	for i := range head {
		head[i] = 0x41
	}
	return append(head, payload...)
}

func gzipBytes(b []byte) []byte {
	var out bytes.Buffer
	w := gzip.NewWriter(&out)
	_, _ = w.Write(b)
	_ = w.Close()
	return out.Bytes()
}

func zlibBytes(b []byte) []byte {
	var out bytes.Buffer
	w := zlib.NewWriter(&out)
	_, _ = w.Write(b)
	_ = w.Close()
	return out.Bytes()
}

func TestParseManifestGzipFraming(t *testing.T) {
	blob := withHeader(t, gzipBytes(manifestBytes(t, sampleManifest())))
	m, err := ParseManifest(blob)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.GetChunks()[0].GetFiles()[0].GetName(); got != "game/bin.exe" {
		t.Fatalf("file name = %q", got)
	}
}

func TestParseManifestZlibFraming(t *testing.T) {
	blob := withHeader(t, zlibBytes(manifestBytes(t, sampleManifest())))
	if _, err := ParseManifest(blob); err != nil {
		t.Fatalf("zlib-framed manifest: %v", err)
	}
}

func TestParseManifestStoredUncompressed(t *testing.T) {
	// No recognised magic: the blind fallback must still find bare protobuf.
	blob := withHeader(t, manifestBytes(t, sampleManifest()))
	if _, err := ParseManifest(blob); err != nil {
		t.Fatalf("stored manifest: %v", err)
	}
}

func TestParseManifestRejectsTruncated(t *testing.T) {
	_, err := ParseManifest(make([]byte, manifestHeaderBytes))
	if err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("err = %v, want a truncation error", err)
	}
}

// A blob that inflates but is not a game manifest must be refused, not returned as an
// empty plan: protobuf accepts almost anything, so "decoded" is not the test.
func TestParseManifestRejectsNonManifestPayload(t *testing.T) {
	blob := withHeader(t, gzipBytes([]byte("this is not a manifest, it is prose")))
	_, err := ParseManifest(blob)
	if !errors.Is(err, ErrManifestUndecodable) {
		t.Fatalf("err = %v, want ErrManifestUndecodable", err)
	}
}

func TestParseManifestRejectsChunksWithNoFiles(t *testing.T) {
	// Well-formed protobuf, zero files anywhere: a framing miss, not a game.
	m := &downloadpb.Manifest{Chunks: []*downloadpb.Chunk{{
		Id: proto.Uint32(7), Type: downloadpb.Chunk_ChunkType_Required.Enum(),
	}}}
	blob := withHeader(t, gzipBytes(manifestBytes(t, m)))
	if _, err := ParseManifest(blob); !errors.Is(err, ErrManifestUndecodable) {
		t.Fatalf("err = %v, want ErrManifestUndecodable", err)
	}
}

// ---------- CDN path derivation ----------

// The rule is empirical, so it is pinned by worked examples rather than re-derived:
// swap the first two hex digits, read a byte, index byte/16 (+16 when odd).
func TestFileHashToPathCharKnownValues(t *testing.T) {
	cases := map[string]byte{
		"00": '0', // swapped 0x00 -> 0  -> alphabet[0]
		"10": 'g', // 0x01 ->   1 -> 1/16=0, odd -> +16 -> alphabet[16]
		"01": '1', // swapped 0x10 -> 16 -> 16/16=1 -> alphabet[1]='1'
		"aa": 'a', // 0xaa -> 170 -> 170/16=10, even    -> alphabet[10]
		"ff": 'v', // swapped 0xff -> 255 -> 15 + 16 = 31 -> alphabet[31]='v'
		"5b": 'r', // 0xb5 -> 181 -> 181/16=11, odd -> +16 -> alphabet[27]
	}
	for prefix, want := range cases {
		hash := prefix + strings.Repeat("0", 38)
		got, err := FileHashToPathChar(hash)
		if err != nil {
			t.Fatalf("%s: %v", prefix, err)
		}
		if got != want {
			t.Errorf("prefix %s -> %q, want %q", prefix, got, want)
		}
	}
}

func TestFileHashToPathCharRejectsNonHex(t *testing.T) {
	for _, bad := range []string{"", "z", "zz" + strings.Repeat("0", 38), "slices_v3/0/AB"} {
		if _, err := FileHashToPathChar(bad); err == nil {
			t.Errorf("FileHashToPathChar(%q) accepted garbage", bad)
		} else {
			var cfgErr *store.ConfigError
			if !errors.As(err, &cfgErr) {
				t.Errorf("want a config error for %q, got %T", bad, err)
			}
		}
	}
}

func TestSliceCdnPathShapeAndCase(t *testing.T) {
	lower := "5b" + strings.Repeat("0", 38)
	path, err := SliceCdnPath(strings.ToUpper(lower))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, "slices_v3/") {
		t.Fatalf("path = %q", path)
	}
	parts := strings.Split(path, "/")
	if len(parts) != 3 || len(parts[2]) != 40 || parts[2] != strings.ToUpper(parts[2]) {
		t.Fatalf("path shape = %q", path)
	}
	// The same hash in either case must land in the same directory.
	also, err := SliceCdnPath(lower)
	if err != nil || also != path {
		t.Fatalf("case-insensitivity: %q vs %q (%v)", also, path, err)
	}
}

func TestSliceCdnPathCandidatesLeadWithComputed(t *testing.T) {
	hash := strings.Repeat("a", 40)
	cands, err := SliceCdnPathCandidates(hash)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 32 {
		t.Fatalf("candidates = %d, want the whole 32-character alphabet", len(cands))
	}
	computed, _ := SliceCdnPath(hash)
	if cands[0] != computed {
		t.Fatalf("first candidate = %q, want the computed %q", cands[0], computed)
	}
	seen := map[string]bool{}
	for _, c := range cands {
		if seen[c] {
			t.Fatalf("duplicate candidate %q", c)
		}
		seen[c] = true
		if !strings.HasSuffix(c, "/"+strings.ToUpper(hash)) {
			t.Fatalf("candidate %q does not carry the hash", c)
		}
	}
}

// ---------- plan building ----------

func planFixture(chunks []*downloadpb.Chunk) *downloadpb.Manifest {
	return &downloadpb.Manifest{Chunks: chunks}
}

func file(name string, size uint64, isDir bool, contentHashes []string, slices []*downloadpb.Slice) *downloadpb.File {
	var slicesBytes [][]byte
	for _, h := range contentHashes {
		b, _ := hex.DecodeString(h)
		slicesBytes = append(slicesBytes, b)
	}
	return &downloadpb.File{
		Name:      proto.String(name),
		Size:      proto.Uint64(size),
		IsDir:     proto.Bool(isDir),
		Slices:    slicesBytes,
		SliceList: slices,
	}
}

func slice(size uint32, downloadSha1 string, offset uint64) *downloadpb.Slice {
	var raw []byte
	if downloadSha1 != "" {
		raw, _ = hex.DecodeString(downloadSha1)
	}
	return &downloadpb.Slice{
		Size:         proto.Uint32(size),
		DownloadSha1: raw,
		FileOffset:   proto.Uint64(offset),
	}
}

const (
	hashA = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	hashB = "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	hashC = "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC"
)

func requiredChunk(language string, files ...*downloadpb.File) *downloadpb.Chunk {
	c := &downloadpb.Chunk{
		Type:  downloadpb.Chunk_ChunkType_Required.Enum(),
		Files: files,
	}
	if language != "" {
		c.Language = proto.String(language)
	}
	return c
}

func optionalChunk(language string, files ...*downloadpb.File) *downloadpb.Chunk {
	return &downloadpb.Chunk{
		Type:     downloadpb.Chunk_ChunkType_Optional.Enum(),
		Language: proto.String(language),
		Files:    files,
	}
}

func TestBuildPlanKeepsRequiredAndWantedLanguageOnly(t *testing.T) {
	m := planFixture([]*downloadpb.Chunk{
		requiredChunk("", file("base.exe", 10, false, []string{hashA}, nil)),
		optionalChunk("en-US", file("audio_en.dat", 5, false, []string{hashB}, nil)),
		optionalChunk("de-DE", file("audio_de.dat", 5, false, []string{hashC}, nil)),
	})
	plan, err := BuildPlan(m, "en-US")
	if err != nil {
		t.Fatal(err)
	}
	if got := planPaths(plan); strings.Join(got, ",") != "audio_en.dat,base.exe" {
		t.Fatalf("plan = %v, want the required file plus the English audio only", got)
	}

	// Asking for German swaps the language file and nothing else.
	planDe, err := BuildPlan(m, "de-DE")
	if err != nil {
		t.Fatal(err)
	}
	if got := planPaths(planDe); strings.Join(got, ",") != "audio_de.dat,base.exe" {
		t.Fatalf("german plan = %v", got)
	}

	// A language nobody asked for yields only required content; this is the filter that
	// stops every voice pack in a title landing in the depot.
	planFr, _ := BuildPlan(m, "fr-FR")
	if got := planPaths(planFr); strings.Join(got, ",") != "base.exe" {
		t.Fatalf("french plan = %v", got)
	}
}

func TestBuildPlanDefaultsLanguage(t *testing.T) {
	m := planFixture([]*downloadpb.Chunk{optionalChunk("en-US", file("a.dat", 1, false, []string{hashA}, nil))})
	plan, err := BuildPlan(m, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Files) != 1 {
		t.Fatalf("empty language should default to en-US, got %d files", len(plan.Files))
	}
}

func TestBuildPlanSkipsDirectories(t *testing.T) {
	m := planFixture([]*downloadpb.Chunk{requiredChunk("",
		file("game", 0, true, nil, nil),
		file("game/bin.exe", 4, false, []string{hashA}, nil),
	)})
	plan, err := BuildPlan(m, "en-US")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Files) != 1 || plan.Files[0].Path != "game/bin.exe" {
		t.Fatalf("plan = %+v, want the directory entry dropped", plan.Files)
	}
}

func TestBuildPlanNormalisesWindowsPaths(t *testing.T) {
	m := planFixture([]*downloadpb.Chunk{requiredChunk("",
		file(`Game\Data\save.bin`, 4, false, []string{hashA}, nil))})
	plan, err := BuildPlan(m, "en-US")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Files[0].Path != "Game/Data/save.bin" {
		t.Fatalf("path = %q, want forward slashes", plan.Files[0].Path)
	}
}

func TestBuildPlanRejectsEscapingPaths(t *testing.T) {
	for _, name := range []string{"../escape.exe", "/etc/passed.exe", `game\..\evil.bin`, "C:/windows/x.exe"} {
		m := planFixture([]*downloadpb.Chunk{requiredChunk("", file(name, 4, false, []string{hashA}, nil))})
		if _, err := BuildPlan(m, "en-US"); err == nil {
			t.Errorf("BuildPlan accepted unsafe path %q", name)
		}
	}
}

func TestBuildPlanKeepsEmptyPlaceholderFiles(t *testing.T) {
	// Live manifests carry zero-byte entries with no slices; the game expects the path.
	m := planFixture([]*downloadpb.Chunk{requiredChunk("", file("data/config/game/dump.txt", 0, false, nil, nil))})
	plan, err := BuildPlan(m, "en-US")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Files) != 1 || plan.Files[0].Size != 0 || len(plan.Files[0].Slices) != 0 {
		t.Fatalf("plan = %+v, want one empty file with no slices", plan.Files)
	}
	if plan.TotalSlices != 0 {
		t.Fatalf("TotalSlices = %d", plan.TotalSlices)
	}
}

func TestBuildPlanRejectsSizedFileWithNoSlices(t *testing.T) {
	m := planFixture([]*downloadpb.Chunk{requiredChunk("", file("big.bin", 1024, false, nil, nil))})
	_, err := BuildPlan(m, "en-US")
	if err == nil || !strings.Contains(err.Error(), "no slices") {
		t.Fatalf("err = %v, want the no-slices rejection", err)
	}
}

func TestBuildPlanTreatsAllZeroOffsetsAsSequential(t *testing.T) {
	m := planFixture([]*downloadpb.Chunk{requiredChunk("", file("game/bin.exe", 30, false,
		[]string{hashA, hashB, hashC},
		[]*downloadpb.Slice{slice(10, "", 0), slice(10, "", 0), slice(10, "", 0)}))})
	plan, err := BuildPlan(m, "en-US")
	if err != nil {
		t.Fatal(err)
	}
	want := []uint64{0, 10, 20}
	for i, s := range plan.Files[0].Slices {
		if s.Offset != want[i] {
			t.Fatalf("slice %d offset = %d, want %d (all-zero offsets mean sequential)", i, s.Offset, want[i])
		}
	}
	// With no downloadSha1 the content hash is what gets fetched and verified.
	if plan.Files[0].Slices[1].VerifySha1 != hashB {
		t.Fatalf("verify hash = %q", plan.Files[0].Slices[1].VerifySha1)
	}
	if s := plan.Files[0].Slices[1]; !strings.Contains(s.CDNPath, hashB) {
		t.Fatalf("cdn path should carry the content hash: %q", s.CDNPath)
	}
}

func TestBuildPlanKeepsExplicitOffsets(t *testing.T) {
	// A patched file can receive slices at named positions, out of order.
	m := planFixture([]*downloadpb.Chunk{requiredChunk("", file("game/bin.exe", 30, false,
		[]string{hashA, hashB},
		[]*downloadpb.Slice{slice(10, "", 20), slice(10, "", 0)}))})
	plan, err := BuildPlan(m, "en-US")
	if err != nil {
		t.Fatal(err)
	}
	got := plan.Files[0].Slices
	if got[0].Offset != 20 || got[1].Offset != 0 {
		t.Fatalf("offsets = %d,%d, want the declared 20,0", got[0].Offset, got[1].Offset)
	}
}

func TestBuildPlanUsesDownloadHashForPathButContentHashToVerify(t *testing.T) {
	// A slice delivered under a different identity than its content: fetch by one,
	// verify by the other. Getting these backwards writes corrupt files that hash-check
	// failures only find later.
	m := planFixture([]*downloadpb.Chunk{requiredChunk("", file("game/bin.exe", 10, false,
		[]string{hashA}, []*downloadpb.Slice{slice(10, hashB, 0)}))})
	plan, err := BuildPlan(m, "en-US")
	if err != nil {
		t.Fatal(err)
	}
	s := plan.Files[0].Slices[0]
	if !strings.Contains(s.CDNPath, hashB) {
		t.Fatalf("cdn path should use the download hash: %q", s.CDNPath)
	}
	if s.VerifySha1 != hashA {
		t.Fatalf("verify hash = %q, want the content hash %q", s.VerifySha1, hashA)
	}
}

func TestBuildPlanRejectsDescriptorHashMismatch(t *testing.T) {
	m := planFixture([]*downloadpb.Chunk{requiredChunk("", file("x.bin", 20, false,
		[]string{hashA, hashB}, []*downloadpb.Slice{slice(10, "", 0)}))})
	_, err := BuildPlan(m, "en-US")
	if err == nil || !strings.Contains(err.Error(), "descriptors for") {
		t.Fatalf("err = %v, want the count-mismatch rejection", err)
	}
}

func TestBuildPlanRejectsMultiHashWithoutDescriptors(t *testing.T) {
	m := planFixture([]*downloadpb.Chunk{requiredChunk("", file("x.bin", 20, false,
		[]string{hashA, hashB}, nil))})
	if _, err := BuildPlan(m, "en-US"); err == nil {
		t.Fatal("want the no-descriptors rejection")
	}
}

func TestBuildPlanRejectsNonHashSlices(t *testing.T) {
	// A short/absent hash cannot address a CDN path or verify a body. Left unchecked
	// this is what crashed the predecessor's worker pool with a raw range error.
	m := planFixture([]*downloadpb.Chunk{requiredChunk("", file("x.bin", 10, false,
		[]string{"abcd"}, nil))})
	_, err := BuildPlan(m, "en-US")
	if err == nil || !strings.Contains(err.Error(), "no content hash") {
		t.Fatalf("err = %v, want the bad-hash rejection", err)
	}
}

func TestPlanTotalsAndUniqueSlices(t *testing.T) {
	// Three files, two distinct slices: the shared one must be fetched once.
	m := planFixture([]*downloadpb.Chunk{requiredChunk("",
		file("a.bin", 10, false, []string{hashA}, nil),
		file("b.bin", 10, false, []string{hashA}, nil),
		file("c.bin", 5, false, []string{hashB}, nil),
	)})
	plan, err := BuildPlan(m, "en-US")
	if err != nil {
		t.Fatal(err)
	}
	if plan.TotalBytes != 25 {
		t.Fatalf("TotalBytes = %d, want 25", plan.TotalBytes)
	}
	if plan.TotalSlices != 3 {
		t.Fatalf("TotalSlices = %d, want 3", plan.TotalSlices)
	}
	unique := plan.UniqueSlices()
	if len(unique) != 2 {
		t.Fatalf("unique slices = %d, want 2 (shared slice fetched once)", len(unique))
	}
	if unique[0].CDNPath == unique[1].CDNPath {
		t.Fatal("dedupe left a duplicate")
	}
}

func TestBuildPlanEmptyManifest(t *testing.T) {
	plan, err := BuildPlan(&downloadpb.Manifest{}, "en-US")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Files) != 0 || plan.TotalBytes != 0 {
		t.Fatalf("plan = %+v, want empty", plan)
	}
}

func planPaths(p Plan) []string {
	out := make([]string, 0, len(p.Files))
	for _, f := range p.Files {
		out = append(out, f.Path)
	}
	// insertion order is chunk order; sort for stable assertions
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
