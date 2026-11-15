# 0005: Desktop UI on Vue 3 and Vite, Tauri Rust core kept

- Status: Accepted
- Date: 2026-11-15
- Deciders: maintainers

## Context

The desktop client is Tauri 2. About 17,000 lines of Rust handle pairing, the download
manager, launching, process management, the importer, saves and cloud sync, mesh
networking and local state. The UI in `desktop/main` is Nuxt 4 with about 7,500 lines of
Vue across 50 files.

Nuxt is used for file-based routing, auto-imports, layouts and a few composables. The
coupling is thin: 9 `definePageMeta` calls, 20 `<NuxtLink>`, 12 `useState`, and a small
number of router and composable uses. There is no server-side rendering need inside a
Tauri window.

Keeping Nuxt means keeping a meta-framework whose main value, SSR and server routes, is
unused, and whose version churn is outside the project's control.

## Decision

Rebuild the desktop UI on Vue 3 and Vite, keep the Rust core unchanged.

- Vue 3, TypeScript, `vite build`, `vue-router`.
- `unplugin-vue-router` keeps `pages/` as file-based routes; route meta moves into
  `<route>` blocks.
- `unplugin-auto-import` and `unplugin-vue-components` keep the Nuxt-style auto-imports.
- A `NuxtLink` shim renders `RouterLink`, so link sites do not change.
- VueUse `createGlobalState` replaces `useState`.
- shadcn-vue on Reka UI supplies components, with the existing `ink` and `accent` scales
  mapped to shadcn CSS variables in Tailwind v4.
- `@headlessui/vue` components are replaced per screen as shadcn equivalents arrive, not
  in one sweep. `@heroicons/vue` stays until a later icon pass.
- `tauri.conf.json` points `frontendDist` at the Vite output.
- Tauri commands, capabilities and the Rust modules are not rewritten in this phase.

## Consequences

- No Nuxt and no Nitro anywhere in the project.
- The 50 existing screens are migrated with shims rather than rewritten, so the risk is
  mostly mechanical.
- Two things get maintained instead of one: explicit route definitions (generated from
  `pages/`) and auto-import configuration. Both are small and checked in.
- shadcn-vue components are copied into the tree. We own them, which means updates are
  manual but there is no version lock.
- Tailwind v4 is new enough that some plugins may need workarounds. If migration shows
  utility churn, the desktop may pin Tailwind 3.4 while the dashboard stays on v4. That is
  a local decision, not a project-wide one.
- The UI and the dashboard stay visually consistent through tokens, not shared code.

## Alternatives considered

- **Svelte 5 and Vite.** Cleaner runtime and smaller bundle, but a full UI rewrite plus new
  component, icon and drag-and-drop libraries.
- **Leptos in Tauri.** One language end to end. Highest cost, WASM toolchain, slow builds,
  and every control rewritten.
- **egui or Slint, dropping Tauri.** Native Rust GUI, but loses the Tauri plugin set, tray,
  deep links, autostart and window chrome that the client depends on.
- **Keep the Nuxt frontend.** No rewrite, but Nuxt survives inside the client, which is
  what this decision exists to prevent.

## References

- Upstream `desktop/main/`, `desktop/src-tauri/`.
- ADR-0002, ADR-0006.
