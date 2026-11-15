<script setup lang="ts">
import { ref } from "vue";

const message = ref("GameDepot desktop scaffold");
</script>

<template>
  <main class="min-h-screen bg-zinc-950 text-zinc-100 flex items-center justify-center">
    <h1 class="text-2xl font-semibold">{{ message }}</h1>
  </main>
</template>
