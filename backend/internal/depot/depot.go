package depot

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"golang.org/x/text/unicode/norm"
)

const ChunkSize = 4 * 1024 * 1024
const MaxFiles = 200000
const MaxTotalBytes = 4 * 1024 * 1024 * 1024 * 1024

type ManifestFile struct {
	Path   string         `json:"path"`
	Size   int64          `json:"size"`
	SHA256 string         `json:"sha256"`
	Mode   int            `json:"mode"`
	Chunks []ManifestChunk `json:"chunks"`
}

type ManifestChunk struct {
	SHA256 string `json:"sha256"`
	Offset int64  `json:"offset"`
	Size   int64  `json:"size"`
}

type ManifestObject struct {
	Size       int64  `json:"size"`
	StoredSize int64  `json:"storedSize"`
	Nonce      string `json:"nonce"`
}

type Manifest struct {
	Format    string                    `json:"format"`
	Version   int                      `json:"version"`
	ChunkSize int                      `json:"chunkSize"`
	TotalSize int64                    `json:"totalSize"`
	Key       string                   `json:"key"`
	Files     []ManifestFile           `json:"files"`
	Objects   map[string]ManifestObject `json:"objects"`
	Digest    string                   `json:"digest"`
}

type BuildProgress struct {
	DoneBytes  int64
	TotalBytes int64
}

func canonical(m Manifest, withDigest bool) ([]byte, error) {
	if !withDigest {
		m.Digest = ""
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

func safePath(p string) bool {
	if p == "" {
		return false
	}
	p = norm.NFC.String(p)
	if strings.HasPrefix(p, "/") {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	if strings.Contains(p, "\\") {
		return false
	}
	for _, r := range p {
		if r == 0 {
			return false
		}
	}
	return true
}

func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	f, err := os.OpenFile(tmp, os.O_RDWR, 0o644)
	if err == nil {
		f.Sync()
		f.Close()
	}
	return os.Rename(tmp, path)
}

// BuildRelease walks sourceDir and publishes a gamedepot-release v1 into depotRoot.
// onProgress is called with (doneBytes, totalBytes) as chunks are written.
func BuildRelease(sourceDir, depotRoot string, onProgress func(int64, int64)) (string, error) {
	sourceDir = filepath.Clean(sourceDir)

	type fileInfo struct {
		abs  string
		rel  string
		size int64
		mode int
	}
	var files []fileInfo
	var totalSize int64

	err := filepath.WalkDir(sourceDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}
		rel = norm.NFC.String(filepath.ToSlash(rel))
		if !safePath(rel) {
			return fmt.Errorf("unsafe path: %s", rel)
		}
		files = append(files, fileInfo{abs: path, rel: rel, size: info.Size(), mode: int(info.Mode() & 0o777)})
		totalSize += info.Size()
		if len(files) > MaxFiles {
			return fmt.Errorf("too many files (max %d)", MaxFiles)
		}
		if totalSize > MaxTotalBytes {
			return fmt.Errorf("source too large")
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].rel < files[j].rel })

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return "", err
	}

	objects := map[string]ManifestObject{}
	var doneBytes int64
	var mu sync.Mutex

	manifest := Manifest{
		Format:    "gamedepot-release",
		Version:   1,
		ChunkSize: ChunkSize,
		TotalSize: totalSize,
		Key:       base64.StdEncoding.EncodeToString(key),
		Files:     []ManifestFile{},
		Objects:   objects,
	}

	for _, f := range files {
		fh, err := os.Open(f.abs)
		if err != nil {
			return "", err
		}
		var chunks []ManifestChunk
		fileHasher := sha256.New()
		buf := make([]byte, ChunkSize)
		var offset int64
		for {
			n, err := io.ReadFull(fh, buf)
			if n > 0 {
				chunkData := append([]byte(nil), buf[:n]...)
				fileHasher.Write(chunkData)
				sum := sha256.Sum256(chunkData)
				chunkHex := hex.EncodeToString(sum[:])

				mu.Lock()
				_, exists := objects[chunkHex]
				mu.Unlock()
				if !exists {
					nonce := make([]byte, 12)
					if _, err := rand.Read(nonce); err != nil {
						fh.Close()
						return "", err
					}
					block, err := aes.NewCipher(key)
					if err != nil {
						fh.Close()
						return "", err
					}
					aead, err := cipher.NewGCM(block)
					if err != nil {
						fh.Close()
						return "", err
					}
					aad := []byte("gamedepot-release:v1:" + chunkHex)
					ct := aead.Seal(nil, nonce, chunkData, aad)

					objPath := filepath.Join(depotRoot, "objects", chunkHex[:2], chunkHex)
					if _, err := os.Stat(objPath); os.IsNotExist(err) {
						if err := writeFileAtomic(objPath, ct); err != nil {
							fh.Close()
							return "", err
						}
					}
					mu.Lock()
					objects[chunkHex] = ManifestObject{
						Size:       int64(n),
						StoredSize: int64(len(ct)),
						Nonce:      base64.StdEncoding.EncodeToString(nonce),
					}
					mu.Unlock()
				}

				chunks = append(chunks, ManifestChunk{
					SHA256: chunkHex,
					Offset:  offset,
					Size:    int64(n),
				})
				offset += int64(n)
				doneBytes += int64(n)
				if onProgress != nil {
					onProgress(doneBytes, totalSize)
				}
			}
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}
			if err != nil {
				fh.Close()
				return "", err
			}
		}
		fh.Close()

		manifest.Files = append(manifest.Files, ManifestFile{
			Path:   f.rel,
			Size:   f.size,
			SHA256: hex.EncodeToString(fileHasher.Sum(nil)),
			Mode:   f.mode,
			Chunks: chunks,
		})
	}

	manifest.Objects = objects

	canon, err := canonicalManifest(manifest, false)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canon)
	manifest.Digest = hex.EncodeToString(digest[:])

	out, err := canonicalManifest(manifest, true)
	if err != nil {
		return "", err
	}
	manifestPath := filepath.Join(depotRoot, "manifests", manifest.Digest+".json")
	if err := writeFileAtomic(manifestPath, out); err != nil {
		return "", err
	}

	return manifest.Digest, nil
}

func canonicalManifest(m Manifest, withDigest bool) ([]byte, error) {
	if !withDigest {
		m.Digest = ""
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

// VerifyRelease checks a manifest's digest and every object's plaintext hash.
func VerifyRelease(depotRoot string, m Manifest) error {
	canon, err := canonicalManifest(m, false)
	if err != nil {
		return err
	}
	h := sha256.Sum256(canon)
	if hex.EncodeToString(h[:]) != m.Digest {
		return fmt.Errorf("digest mismatch")
	}
	if m.Format != "gamedepot-release" || m.Version != 1 {
		return fmt.Errorf("bad format/version")
	}
	block, err := aes.NewCipher(mustDecodeKey(m.Key))
	if err != nil {
		return err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	for shaHex, obj := range m.Objects {
		raw, err := hex.DecodeString(shaHex)
		if err != nil || len(raw) != 32 {
			return fmt.Errorf("bad object key: %s", shaHex)
		}
		nonce, err := base64.StdEncoding.DecodeString(obj.Nonce)
		if err != nil || len(nonce) != 12 {
			return fmt.Errorf("bad nonce for %s", shaHex)
		}
		path := filepath.Join(depotRoot, "objects", shaHex[:2], shaHex)
		ct, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("missing object %s: %w", shaHex, err)
		}
		if int64(len(ct)) != obj.StoredSize {
			return fmt.Errorf("size mismatch for %s", shaHex)
		}
		aad := []byte("gamedepot-release:v1:" + shaHex)
		pt, err := aead.Open(nil, nonce, ct, aad)
		if err != nil {
			return fmt.Errorf("decrypt failed for %s: %w", shaHex, err)
		}
		if int64(len(pt)) != obj.Size {
			return fmt.Errorf("plaintext size mismatch for %s", shaHex)
		}
		h := sha256.Sum256(pt)
		if hex.EncodeToString(h[:]) != shaHex {
			return fmt.Errorf("content hash mismatch for %s", shaHex)
		}
	}
	// verify file chunk references exist
	for _, f := range m.Files {
		for _, ch := range f.Chunks {
			if _, ok := m.Objects[ch.SHA256]; !ok {
				return fmt.Errorf("file %s references unknown chunk %s", f.Path, ch.SHA256)
			}
		}
	}
	return nil
}

func mustDecodeKey(b64 string) []byte {
	b, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		panic(err)
	}
	return b
}

// DecryptObject returns plaintext for a single object, verifying its hash.
func DecryptObject(depotRoot string, m Manifest, shaHex string) ([]byte, error) {
	obj, ok := m.Objects[shaHex]
	if !ok {
		return nil, fmt.Errorf("unknown object")
	}
	key, err := base64.StdEncoding.DecodeString(m.Key)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce, err := base64.StdEncoding.DecodeString(obj.Nonce)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(depotRoot, "objects", shaHex[:2], shaHex)
	ct, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	aad := []byte("gamedepot-release:v1:" + shaHex)
	pt, err := aead.Open(nil, nonce, ct, aad)
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256(pt)
	if hex.EncodeToString(h[:]) != shaHex {
		return nil, fmt.Errorf("content hash mismatch")
	}
	return pt, nil
}
