# 0006: Desktop client: Tauri with Vue 3 and Vite

- Status: Accepted
- Date: 2026-11-15
- Deciders: maintainers

## Context

The desktop client installs and launches games on Windows and Linux. It needs a UI and
a core that can download, verify, decrypt and assemble large releases.

## Decision

- Tauri 2 provides the shell and the command bridge.
- The UI is Vue 3, Vite, TypeScript, vue-router and shadcn-vue on Reka UI, with
  Tailwind v4.
- The Rust core is written for this project. Its modules are `api`, `auth`, `download`,
  `install`, `launch` and `settings`.
- Token storage uses the OS keyring. Install state uses a local SQLite database that
  never holds credentials.

## Consequences

- No meta-framework and no SSR. The UI is a static build consumed by Tauri.
- The Rust core is a real implementation, not a wrapper around another project.
- shadcn-vue components are copied into the tree and owned by us.
- Tailwind v4 may need workarounds for older plugins. The desktop may pin 3.4 while the
  dashboard stays on v4.

## Alternatives considered

- **Svelte 5.** Cleaner runtime, but a full rewrite of the component set.
- **Leptos.** One language end to end, at the cost of a WASM toolchain and slow builds.
- **egui or Slint.** Native GUI, but the Tauri plugin set, tray and deep links would
  have to be rebuilt.
