<template>
  <div></div>
</template>

<script setup lang="ts">
import type { Settings } from "~/server/internal/utils/types";

useHead({
  title: "Home",
});

const router = useRouter();

const redirectTo = ref("/library");

onMounted(async () => {
  try {
    const settings = await $dropFetch<Settings>("/api/v1/settings");
    redirectTo.value = settings?.store?.enabled ? "/store" : "/library";
  } catch (e) {
    redirectTo.value = "/library";
  }
  router.push(redirectTo.value);
});

definePageMeta({
  // Fallback redirect; overridden at runtime above.
  redirect: "/library",
});
</script>
