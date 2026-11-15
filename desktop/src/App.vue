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

const serverUrl = ref("");
const status = ref("Checking...");
const me = ref<Me | null>(null);
const library = ref<LibraryItem[]>([]);
const pairCode = ref("");
const busy = ref(false);
const error = ref("");

async function refresh() {
  busy.value = true;
  error.value = "";
  try {
    me.value = await invoke<Me | null>("signed_in");
    status.value = me.value ? `Signed in as ${me.value.username}` : "Not signed in";
    if (me.value) {
      library.value = await invoke<LibraryItem[]>("library_list");
    } else {
      library.value = [];
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
  </main>
</template>
