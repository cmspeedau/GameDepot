<script setup lang="ts">
import { onMounted, ref } from "vue";
import { invoke } from "@tauri-apps/api/core";

type Me = { id: string; username: string; displayName: string };
type LibraryItem = {
  id: string;
  name: string;
  summary: string;
  coverUrl: string | null;
  installedVersionId: string | null;
};
type PairingInitiate = { code: string; expiresAt: string };
type RootInfo = { label: string; path: string; recursive: boolean; exists: boolean };
type SaveVersion = {
  id: string;
  seq: number;
  kind: string;
  contentHash: string;
  size: number;
  fileCount: number;
  pinned: boolean;
  createdAt: string;
};
type SyncSummary =
  | { kind: "noop"; seq: number; contentHash: string }
  | { kind: "pushed"; seq: number; contentHash: string; fileCount: number }
  | { kind: "pulled"; seq: number; contentHash: string; filesWritten: number }
  | {
      kind: "conflict";
      serverSeq: number;
      serverContentHash: string;
      localContentHash: string;
    };

const serverUrl = ref("");
const status = ref("Checking...");
const me = ref<Me | null>(null);
const library = ref<LibraryItem[]>([]);
const pairCode = ref("");
const busy = ref(false);
const error = ref("");

const saveGame = ref("");
const saveSlot = ref(0);
const saveRoots = ref<RootInfo[] | null>(null);
const saveVersions = ref<SaveVersion[]>([]);
const syncResult = ref<SyncSummary | null>(null);
const syncMessage = ref("");

async function refresh() {
  busy.value = true;
  error.value = "";
  try {
    me.value = await invoke<Me | null>("signed_in");
    status.value = me.value ? `Signed in as ${me.value.username}` : "Not signed in";
    if (me.value) {
      library.value = await invoke<LibraryItem[]>("library_list");
      if (!library.value.some((g) => g.id === saveGame.value)) {
        saveGame.value = "";
        saveRoots.value = null;
        saveVersions.value = [];
        syncResult.value = null;
      }
    } else {
      library.value = [];
      saveGame.value = "";
      saveRoots.value = null;
      saveVersions.value = [];
      syncResult.value = null;
    }
  } catch (e) {
    status.value = "Not connected";
    error.value = String(e);
  } finally {
    busy.value = false;
  }
}

async function saveServer() {
  await invoke("settings_set", { settings: { serverUrl: serverUrl.value } });
  await refresh();
}

async function startPair() {
  busy.value = true;
  error.value = "";
  try {
    const r = await invoke<PairingInitiate>("pair_initiate", {
      name: "desktop",
      platform: "linux",
    });
    pairCode.value = r.code;
  } catch (e) {
    error.value = String(e);
  } finally {
    busy.value = false;
  }
}

async function finishPair() {
  if (!pairCode.value) return;
  busy.value = true;
  error.value = "";
  try {
    await invoke("pair_complete", { code: pairCode.value });
    pairCode.value = "";
    await refresh();
  } catch (e) {
    error.value = String(e);
  } finally {
    busy.value = false;
  }
}

async function signOut() {
  await invoke("sign_out");
  await refresh();
}

async function inspectSaves() {
  if (!saveGame.value) return;
  busy.value = true;
  error.value = "";
  syncResult.value = null;
  syncMessage.value = "";
  try {
    saveRoots.value = await invoke<RootInfo[]>("save_roots", { gameId: saveGame.value });
    await loadVersions();
  } catch (e) {
    saveRoots.value = null;
    saveVersions.value = [];
    error.value = String(e);
  } finally {
    busy.value = false;
  }
}

async function loadVersions() {
  // A slot that was never synced answers 404; that is an empty history, not an error.
  try {
    saveVersions.value = await invoke<SaveVersion[]>("save_versions", {
      gameId: saveGame.value,
      slot: saveSlot.value,
    });
  } catch {
    saveVersions.value = [];
  }
}

async function syncNow() {
  busy.value = true;
  error.value = "";
  syncMessage.value = "Syncing...";
  try {
    syncResult.value = await invoke<SyncSummary>("save_sync_now", {
      gameId: saveGame.value,
      slot: saveSlot.value,
    });
    syncMessage.value = "";
    await loadVersions();
  } catch (e) {
    syncMessage.value = "";
    error.value = String(e);
  } finally {
    busy.value = false;
  }
}

async function takeServer() {
  busy.value = true;
  error.value = "";
  try {
    syncResult.value = await invoke<SyncSummary>("save_take_server", {
      gameId: saveGame.value,
      slot: saveSlot.value,
    });
    await loadVersions();
  } catch (e) {
    error.value = String(e);
  } finally {
    busy.value = false;
  }
}

async function takeLocal() {
  busy.value = true;
  error.value = "";
  try {
    syncResult.value = await invoke<SyncSummary>("save_take_local", {
      gameId: saveGame.value,
      slot: saveSlot.value,
    });
    await loadVersions();
  } catch (e) {
    error.value = String(e);
  } finally {
    busy.value = false;
  }
}

async function restoreVersion(v: SaveVersion) {
  busy.value = true;
  error.value = "";
  try {
    await invoke("save_restore", {
      gameId: saveGame.value,
      slot: saveSlot.value,
      versionId: v.id,
    });
    await loadVersions();
    await takeServer();
  } catch (e) {
    error.value = String(e);
  } finally {
    busy.value = false;
  }
}

async function togglePin(v: SaveVersion) {
  busy.value = true;
  error.value = "";
  try {
    await invoke("save_pin", {
      gameId: saveGame.value,
      slot: saveSlot.value,
      versionId: v.id,
      pinned: !v.pinned,
    });
    await loadVersions();
  } catch (e) {
    error.value = String(e);
  } finally {
    busy.value = false;
  }
}

async function deleteVersion(v: SaveVersion) {
  busy.value = true;
  error.value = "";
  try {
    await invoke("save_delete_version", {
      gameId: saveGame.value,
      slot: saveSlot.value,
      versionId: v.id,
    });
    await loadVersions();
  } catch (e) {
    error.value = String(e);
  } finally {
    busy.value = false;
  }
}

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KiB`;
  return `${(n / 1024 / 1024).toFixed(1)} MiB`;
}

onMounted(async () => {
  const s = await invoke<{ serverUrl: string }>("settings_get");
  serverUrl.value = s.serverUrl;
  await refresh();
});
</script>

<template>
  <main class="mx-auto max-w-2xl p-6 font-sans text-neutral-100">
    <h1 class="mb-4 text-2xl font-semibold">GameDepot</h1>

    <section class="mb-6 flex flex-wrap items-center gap-2">
      <input
        v-model="serverUrl"
        placeholder="https://your-server"
        class="flex-1 rounded border border-neutral-700 bg-neutral-900 px-3 py-2"
      />
      <button class="rounded bg-neutral-100 px-4 py-2 font-medium text-neutral-900" @click="saveServer">
        Save
      </button>
      <button class="rounded border border-neutral-700 px-4 py-2" :disabled="busy" @click="refresh">
        Refresh
      </button>
      <button v-if="me" class="rounded border border-neutral-700 px-4 py-2" @click="signOut">
        Sign out
      </button>
    </section>

    <p class="mb-2 text-sm text-neutral-400">{{ status }}</p>
    <p v-if="error" class="mb-4 rounded bg-red-900/40 px-3 py-2 text-sm text-red-200">
      {{ error }}
    </p>

    <section v-if="!me" class="mb-6">
      <h2 class="mb-2 text-lg font-medium">Pair this device</h2>
      <div class="flex items-center gap-2">
        <button class="rounded bg-indigo-600 px-4 py-2 font-medium" :disabled="busy" @click="startPair">
          Get pairing code
        </button>
        <button
          v-if="pairCode"
          class="rounded border border-neutral-700 px-4 py-2"
          :disabled="busy"
          @click="finishPair"
        >
          I approved <span class="ml-1 font-mono">{{ pairCode }}</span>
        </button>
      </div>
      <p class="mt-2 text-sm text-neutral-400">
        Enter the code in the server dashboard, then press "I approved".
      </p>
    </section>

    <section v-if="me">
      <h2 class="mb-2 text-lg font-medium">Library</h2>
      <ul class="space-y-2">
        <li v-for="g in library" :key="g.id" class="rounded border border-neutral-800 px-3 py-2">
          {{ g.name }}
          <span v-if="g.installedVersionId" class="ml-2 text-xs text-emerald-400">installed</span>
        </li>
        <li v-if="library.length === 0" class="text-sm text-neutral-500">No games yet.</li>
      </ul>
    </section>

    <section v-if="me && library.length" class="mt-6">
      <h2 class="mb-2 text-lg font-medium">Cloud saves</h2>
      <div class="mb-3 flex flex-wrap items-center gap-2">
        <select v-model="saveGame" class="rounded border border-neutral-700 bg-neutral-900 px-3 py-2">
          <option value="" disabled>Select a game</option>
          <option v-for="g in library" :key="g.id" :value="g.id">{{ g.name }}</option>
        </select>
        <input
          v-model.number="saveSlot"
          type="number"
          min="0"
          class="w-20 rounded border border-neutral-700 bg-neutral-900 px-3 py-2"
          title="Slot"
        />
        <button
          class="rounded border border-neutral-700 px-4 py-2"
          :disabled="busy || !saveGame"
          @click="inspectSaves"
        >
          Inspect
        </button>
        <button
          class="rounded bg-indigo-600 px-4 py-2 font-medium"
          :disabled="busy || !saveGame"
          @click="syncNow"
        >
          Sync
        </button>
      </div>

      <p v-if="syncMessage" class="mb-2 text-sm text-neutral-400">{{ syncMessage }}</p>

      <div v-if="syncResult" class="mb-3 rounded border border-neutral-800 px-3 py-2 text-sm">
        <template v-if="syncResult.kind === 'pushed'">
          Pushed {{ syncResult.fileCount }} file(s) as v{{ syncResult.seq }}.
        </template>
        <template v-else-if="syncResult.kind === 'pulled'">
          Pulled v{{ syncResult.seq }}, wrote {{ syncResult.filesWritten }} file(s).
        </template>
        <template v-else-if="syncResult.kind === 'noop'">
          Up to date at v{{ syncResult.seq }}.
        </template>
        <template v-else-if="syncResult.kind === 'conflict'">
          <div class="mb-2 text-amber-300">
            Both sides changed since the last sync (server v{{ syncResult.serverSeq }}).
          </div>
          <div class="flex gap-2">
            <button class="rounded bg-neutral-100 px-3 py-1 text-neutral-900" :disabled="busy" @click="takeServer">
              Use server
            </button>
            <button class="rounded border border-neutral-700 px-3 py-1" :disabled="busy" @click="takeLocal">
              Keep mine
            </button>
          </div>
        </template>
      </div>

      <ul v-if="saveRoots" class="mb-3 space-y-1 text-sm text-neutral-400">
        <li v-for="r in saveRoots" :key="r.label" class="flex items-center gap-2">
          <span :class="r.exists ? 'text-emerald-400' : 'text-red-400'">●</span>
          <span class="font-mono">{{ r.path }}</span>
          <span v-if="!r.exists" class="text-xs">(missing)</span>
          <span v-if="!r.recursive" class="text-xs">(top level only)</span>
        </li>
        <li v-if="saveRoots.length === 0">No save paths configured for this game on this platform.</li>
      </ul>

      <table v-if="saveVersions.length" class="w-full text-left text-sm">
        <thead class="text-xs text-neutral-500">
          <tr>
            <th class="py-1">Ver</th>
            <th>Kind</th>
            <th>Size</th>
            <th>When</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="v in saveVersions" :key="v.id" class="border-t border-neutral-800">
            <td class="py-2 font-mono">v{{ v.seq }}</td>
            <td>{{ v.kind }}<span v-if="v.pinned" class="ml-1 text-amber-300">pinned</span></td>
            <td class="oc-num">{{ formatBytes(v.size) }}</td>
            <td class="text-neutral-500">{{ v.createdAt.slice(0, 16).replace("T", " ") }}</td>
            <td class="space-x-2 text-right">
              <button class="text-indigo-400 hover:underline" :disabled="busy" @click="restoreVersion(v)">
                Restore
              </button>
              <button class="text-neutral-400 hover:underline" :disabled="busy" @click="togglePin(v)">
                {{ v.pinned ? "Unpin" : "Pin" }}
              </button>
              <button
                v-if="!v.pinned"
                class="text-red-400 hover:underline"
                :disabled="busy"
                @click="deleteVersion(v)"
              >
                Delete
              </button>
            </td>
          </tr>
        </tbody>
      </table>
      <p v-else-if="saveGame" class="text-sm text-neutral-500">No saved versions yet for slot {{ saveSlot }}.</p>
    </section>
  </main>
</template>
