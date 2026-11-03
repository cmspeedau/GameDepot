import { readFile } from "node:fs/promises";
import { ArkErrors, type } from "arktype";
import { createError, getQuery } from "h3";
import { defineClientEventHandler } from "~/server/internal/clients/event-handler";
import { systemConfig } from "~/server/internal/config/sys-conf";
import prisma from "~/server/internal/db/database";
import { diffDropletManifests, parseDropletManifest } from "~/server/utils/depot/manifest";
import { casManifestPath, depotRootFor, isContentDigest } from "~/server/utils/depot/paths";

const Query = type({ from: "string", to: "string" });

/** Compare two authenticated client-visible v1 manifests and report reusable chunks. */
export default defineClientEventHandler(async (h3) => {
  const query = Query(getQuery(h3));
  if (query instanceof ArkErrors)
    throw createError({ statusCode: 400, message: query.summary });
  const [from, to] = await Promise.all(
    [query.from, query.to].map((versionId) => prisma.gameVersion.findUnique({
      where: { versionId },
      select: { versionId: true, casManifestDigest: true },
    })),
  );
  if (!from || !to)
    throw createError({ statusCode: 404, message: "Version not found" });
  for (const version of [from, to])
    if (!isContentDigest(version.casManifestDigest))
      throw createError({ statusCode: 404, message: `Version ${version.versionId} has no content-addressed manifest` });

  const root = depotRootFor(systemConfig.getDataFolder());
  const read = async (digest: string) => {
    const text = await readFile(casManifestPath(root, digest), "utf8").catch(() => {
      throw createError({ statusCode: 500, message: "A version manifest is missing from the depot store" });
    });
    try { return parseDropletManifest(text); } catch {
      throw createError({ statusCode: 500, message: "A version manifest does not match its own digest" });
    }
  };
  const [oldManifest, newManifest] = await Promise.all([
    read(from.casManifestDigest), read(to.casManifestDigest),
  ]);
  const diff = diffDropletManifests(oldManifest, newManifest);
  return {
    from: from.versionId, to: to.versionId,
    oldManifestDigest: from.casManifestDigest, newManifestDigest: to.casManifestDigest,
    unchangedFiles: diff.unchangedFiles, changedFiles: diff.changedFiles,
    addedFiles: diff.addedFiles, removedFiles: diff.removedFiles,
    reusableChunkHashes: [...diff.reusableChunkHashes],
    chunkHashesToDownload: [...diff.chunkHashesToDownload],
    bytesToDownload: diff.bytesToDownload, totalBytes: diff.totalBytes,
  };
});
