# 0010: GOG platform handling, and why Linux builds are refused rather than downgraded

- Status: Accepted
- Date: 2026-11-18
- Deciders: maintainers

## Context

Ingestion had never acquired Mac or Linux titles on machines that could run them. Two
separate causes, both now measured against production GOG:

1. **Vocabulary.** The library endpoint reports a title's platforms as
   `{ "Windows": true, "Mac": true, "Linux": false }`, the dashboard's platform values are
   `windows|macos|linux`, and the content system's build path is
   `/products/{id}/os/{os}/builds`, which only accepts `windows` and `osx`. Asking it for
   `macos` returns 404 `Unsupported OS`, which the pipeline reported as "no builds", and
   the default was Windows. So a Mac-native title either failed or silently produced a
   Windows release, and nobody could tell which.
2. **Linux genuinely is not on that endpoint.** Measured 2026-11-18: `os=linux` answers
   404 `Unsupported OS` for generation 1 and 2 alike, even for a product whose own store
   record says `content_system_compatibility.linux: true`. Public product data for the
   same title lists one installer each for `windows`, `mac` and `linux`: GOG ships native
   Linux content through the installer downlink API (`/products/{id}/downlink/installer/…`)
   with its XML chunklist, not through the depot/chunk content system the acquisition path
   uses. The predecessor inherited the same behaviour and hid it behind the Windows default.

Live verification, no credentials required (the build, meta and manifest endpoints answer
without a token): Mac resolves build `57325296727241979` version `2.1a`, 3 depots,
576 files, with `.app/Contents/MacOS/` paths inside. Windows resolves as before.

## Decision

- One mapping, one place: `gog.ContentOS` translates caller platform words to the value
  GOG's path accepts. `macos`, `mac`, `osx` and `darwin` all mean `osx`. The library's
  `worksOn` flags are normalised the other way by `gog.PlatformCanonical`, so `Mac` becomes
  `macos` and every layer speaks the project's vocabulary.
- Linux is refused with an explanatory error **before any request is made**. Never fall
  back to Windows behind the caller's back; that fallback is what made this invisible.
- The acquired build meta carries `platform` (`windows` or `osx`). Acquisition compares it
  with what was requested and stops on a mismatch, so a future vocabulary change produces
  an error instead of the wrong operating system's files.
- The dashboard offers only the platforms GOG reports for that title, and labels the Linux
  option as having no depot builds.
- Native Linux acquisition is not implemented. It needs a second route: resolve the
  product's Linux installer, fetch its downlink, stream the installer file and parse the
  chunklist XML for per-chunk MD5s, then either extract the installer (innoextract, `mojo`,
  7z as the predecessor did) or, where the payload is a direct archive, unpack it. That is
  deferred as its own piece of work, tracked in the roadmap, not smuggled into this fix.

## Consequences

- Mac titles become ingestible today, with the depot path, verification and release
  building that already work for Windows.
- Because the product mapping is keyed per platform, ingesting a title's Mac build after
  its Windows build would have forked a second catalogue entry for the same game.
  `GetStoreProductSiblings` lets ingest follow an existing mapping for another platform,
  so one title is one game with one version per build. The sibling read is allowed to
  fail: the cost of losing it is a duplicate row, not a lost download. Chunk downloads still require a real account
  because `secure_link` returns 401 without one, so the last step is only provable with
  an operator's connection.
- A Linux operator gets one of two honest outcomes: pick the Windows build and run it
  through the compatibility tool, or wait for the installer route. The error text names
  both.
- Mac launch entries come from the same game info document as Windows ones. Whether GOG
  declares Mac executables in the `type: "Game"` shape or nests them per platform is not
  yet confirmed against a live Mac build; if it differs, the mapping needs one more case,
  and it will show up as a version with no launches rather than a broken install.
- Depot objects carry no per-depot operating-system field (checked against live builds:
  keys are `compressedSize, languages, manifest, productId, size`), so filtering depots by
  platform would be inventing a field. The build request is what scopes the platform.

## Alternatives considered

- **Default `osx` to Windows when the Mac path 404s.** Rejected: it reproduces the bug
  that hid this for as long as it lasted.
- **Send `linux` anyway and treat the 404 as "no builds".** It would read as a data
  problem with the title rather than a limitation of this adapter, which is the wrong
  signal for someone deciding whether to ingest.
- **Implement the installer route in this change.** It is a different protocol surface
  with different verification (chunklist XML, installer extraction), and bundling it would
  delay the Mac fix that is already proven.

## References

- ADR-0009 (the archived predecessor as a specification).
- `gogapidocs.readthedocs.io/en/latest/galaxy.html`, content-system.gog.com section.
- `internal/store/gog/live_test.go` for the live probes behind `GAMEDEPOT_LIVE_GOG=1`.
