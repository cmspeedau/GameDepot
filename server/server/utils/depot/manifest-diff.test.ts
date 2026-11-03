import { test } from "node:test";
import assert from "node:assert/strict";
import { createDropletManifest, diffDropletManifests, type DropletManifest } from "./manifest";
import type { ChunkFileResult } from "./chunker";

function file(path: string, digest: string, chunks: string[] = [digest]) {
  let offset = 0;
  const records = chunks.map((sha256, index) => {
    const size = Buffer.byteLength(sha256);
    const chunk = { index, offset, size, sha256, object: `objects/${sha256.slice(0, 2)}/${sha256}` };
    offset += size;
    return chunk;
  });
  const content: ChunkFileResult = { size: Buffer.byteLength(digest), sha256: digest, chunks: records };
  return { path, content };
}
const manifest = (...files: ReturnType<typeof file>[]) => createDropletManifest(files);

test("identical manifests have no changes or downloads", () => {
  const m = manifest(file("a", "same", ["abc", "def"]));
  const d = diffDropletManifests(m, m);
  assert.equal(d.unchangedFiles.length, 1);
  assert.equal(d.changedFiles.length + d.addedFiles.length + d.removedFiles.length, 0);
  assert.deepEqual([...d.reusableChunkHashes].sort(), ["abc", "def"]);
  assert.deepEqual([...d.chunkHashesToDownload], []);
  assert.equal(d.bytesToDownload, 0);
  assert.equal(d.totalBytes, 6);
});
test("all-new release downloads every chunk", () => {
  const d = diffDropletManifests(manifest(), manifest(file("new", "new")));
  assert.deepEqual(d.addedFiles.map(f => f.path), ["new"]);
  assert.deepEqual([...d.chunkHashesToDownload], ["new"]);
  assert.equal(d.bytesToDownload, 3);
  assert.equal(d.totalBytes, 3);
});
test("partial chunk overlap downloads only missing hashes", () => {
  const d = diffDropletManifests(manifest(file("old", "abcdef", ["abc", "def"])), manifest(file("new", "defghi", ["def", "ghi"])));
  assert.deepEqual([...d.reusableChunkHashes], ["def"]);
  assert.deepEqual([...d.chunkHashesToDownload], ["ghi"]);
  assert.equal(d.bytesToDownload, 3);
  assert.equal(d.totalBytes, 6);
});
test("renamed file is added and removed while retaining shared chunks", () => {
  const d = diffDropletManifests(manifest(file("before", "payload", ["pay", "load"])), manifest(file("after", "payload", ["pay", "load"])));
  assert.deepEqual(d.addedFiles.map(f => f.path), ["after"]);
  assert.deepEqual(d.removedFiles.map(f => f.path), ["before"]);
  assert.equal(d.reusableChunkHashes.size, 2);
  assert.equal(d.bytesToDownload, 0);
});
test("removed files need no downloads", () => {
  const d = diffDropletManifests(manifest(file("gone", "gone")), manifest());
  assert.deepEqual(d.removedFiles.map(f => f.path), ["gone"]);
  assert.equal(d.totalBytes, 0);
  assert.equal(d.bytesToDownload, 0);
});
test("non-v1 manifest versions are rejected clearly", () => {
  const valid = manifest();
  const invalid = { ...valid, version: 2 } as unknown as DropletManifest;
  assert.throws(() => diffDropletManifests(invalid, valid), /oldManifest.*v1/);
  assert.throws(() => diffDropletManifests(valid, invalid), /newManifest.*v1/);
});
