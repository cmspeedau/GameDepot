import { createHash } from "node:crypto";
import { canonicalize } from "json-canonicalize";
import { readFile, writeFile, rename, mkdir } from "node:fs/promises";
import { dirname } from "node:path";
import type { ChunkFileResult } from "./chunker";

export interface DropletFile {
  path: string;
  size: number;
  /**
   * Permission bits (`mode & 0o7777`). A client assembles the install itself
   * from chunks, so this is where the executable bit survives: without it a
   * Linux build arrives with nothing runnable in it. Sources that carry no
   * meaningful mode (a CDN stream) publish 0o644.
   */
  mode: number;
  sha256: string;
  chunks: ChunkFileResult["chunks"];
}
export interface DropletManifest {
  format: "gamedepot-droplet";
  version: 1;
  files: DropletFile[];
  chunkIndex: Record<string, { object: string; size: number }>;
  digest: string;
}
/** Create deterministic JSON manifest; file paths must be relative and traversal-free. */
export function createDropletManifest(
  files: Array<{ path: string; content: ChunkFileResult; mode?: number }>,
): DropletManifest {
  const seen = new Set<string>(),
    chunkIndex: DropletManifest["chunkIndex"] = {};
  const normalized = [...files]
    .sort((a, b) => a.path.localeCompare(b.path, "en"))
    .map(({ path, content, mode }) => {
      const p = path.replace(/\\/g, "/").normalize("NFC");
      if (
        !p ||
        p.startsWith("/") ||
        p.split("/").some((x) => !x || x === "." || x === "..") ||
        seen.has(p)
      )
        throw new Error(`Unsafe or duplicate manifest path: ${path}`);
      seen.add(p);
      for (const chunk of content.chunks) {
        const old = chunkIndex[chunk.sha256];
        if (old && (old.size !== chunk.size || old.object !== chunk.object))
          throw new Error(`Conflicting chunk ${chunk.sha256}`);
        chunkIndex[chunk.sha256] = { object: chunk.object, size: chunk.size };
      }
      return {
        path: p,
        size: content.size,
        sha256: content.sha256,
        mode: (mode ?? 0o644) & 0o7777,
        chunks: content.chunks,
      };
    });
  const base = {
    format: "gamedepot-droplet" as const,
    version: 1 as const,
    files: normalized,
    chunkIndex,
  };
  return {
    ...base,
    digest: createHash("sha256").update(canonicalize(base)).digest("hex"),
  };
}
export async function writeDropletManifest(
  path: string,
  manifest: DropletManifest,
): Promise<void> {
  await mkdir(dirname(path), { recursive: true });
  const temp = `${path}.${process.pid}.tmp`;
  await writeFile(temp, `${canonicalize(manifest)}\n`, { flag: "wx" });
  await rename(temp, path);
}
/** Verify a manifest document; throws when its digest or format is not right. */
export function parseDropletManifest(text: string): DropletManifest {
  const m = JSON.parse(text) as DropletManifest;
  const { digest, ...base } = m;
  if (
    m.format !== "gamedepot-droplet" ||
    m.version !== 1 ||
    createHash("sha256").update(canonicalize(base)).digest("hex") !== digest
  )
    throw new Error("Invalid DropletManifest digest or version");
  return m;
}

export async function readDropletManifest(
  path: string,
): Promise<DropletManifest> {
  return parseDropletManifest(await readFile(path, "utf8"));
}

export interface DropletManifestDiff {
  unchangedFiles: DropletFile[];
  changedFiles: Array<{ oldFile: DropletFile; newFile: DropletFile }>;
  addedFiles: DropletFile[];
  removedFiles: DropletFile[];
  reusableChunkHashes: Set<string>;
  chunkHashesToDownload: Set<string>;
  bytesToDownload: number;
  totalBytes: number;
}

/** Compare two v1 manifests by path/digest and content-addressed chunk identity. */
export function diffDropletManifests(
  oldManifest: DropletManifest,
  newManifest: DropletManifest,
): DropletManifestDiff {
  for (const [name, manifest] of [
    ["oldManifest", oldManifest],
    ["newManifest", newManifest],
  ] as const) {
    if (
      !manifest ||
      manifest.format !== "gamedepot-droplet" ||
      manifest.version !== 1
    )
      throw new Error(`${name} must be a gamedepot-droplet v1 manifest`);
  }
  const oldByPath = new Map(oldManifest.files.map((file) => [file.path, file]));
  const newByPath = new Map(newManifest.files.map((file) => [file.path, file]));
  const unchangedFiles: DropletFile[] = [];
  const changedFiles: DropletManifestDiff["changedFiles"] = [];
  const addedFiles: DropletFile[] = [];
  const removedFiles: DropletFile[] = [];
  for (const file of newManifest.files) {
    const oldFile = oldByPath.get(file.path);
    if (!oldFile) addedFiles.push(file);
    else if (oldFile.sha256 === file.sha256) unchangedFiles.push(file);
    else changedFiles.push({ oldFile, newFile: file });
  }
  for (const file of oldManifest.files)
    if (!newByPath.has(file.path)) removedFiles.push(file);
  const oldChunks = new Set(Object.keys(oldManifest.chunkIndex));
  const newChunks = new Set(Object.keys(newManifest.chunkIndex));
  const reusableChunkHashes = new Set(
    [...newChunks].filter((hash) => oldChunks.has(hash)),
  );
  const chunkHashesToDownload = new Set(
    [...newChunks].filter((hash) => !oldChunks.has(hash)),
  );
  const totalBytes = [...newChunks].reduce(
    (sum, hash) => sum + newManifest.chunkIndex[hash]!.size,
    0,
  );
  const bytesToDownload = [...chunkHashesToDownload].reduce(
    (sum, hash) => sum + newManifest.chunkIndex[hash]!.size,
    0,
  );
  return {
    unchangedFiles, changedFiles, addedFiles, removedFiles,
    reusableChunkHashes, chunkHashesToDownload, bytesToDownload, totalBytes,
  };
}
