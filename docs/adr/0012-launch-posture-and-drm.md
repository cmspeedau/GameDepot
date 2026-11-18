# 0012: Launch posture and DRM

- Status: Accepted
- Date: 2026-11-18
- Deciders: maintainers

## Context

A depot release is a tree of files. For some titles that tree is the whole game and
starts when asked. For others it is the game plus an expectation that a storefront is
present, signed in, and believes the game is installed at that path: Ubisoft Connect
and the EA app both check entitlement through their own client, and some titles carry
Denuvo on top.

The primitives for this already exist in this repository, and none of them say what the
policy is:

- `desktop/src-tauri/src/launch.rs` builds a native `execve`, and `umu_launch_spec`
  builds an umu-launcher command carrying `WINEPREFIX`, `GAMEID` and `STORE`. umu is
  the layer that already understands store-specific runtime wiring.
- `runner.rs` discovers umu and Proton-GE builds and can install GE-Proton;
  `prefix.rs` gives a prefix per game and profile.
- `runner_recipes` (migration `00006`) carries a `profile` jsonb, a `source`, and a
  `compatibility` state defaulting to `untested`.
  `GET /api/v1/games/{id}/recipe` serves it to paired clients.

Three gaps sit underneath that. `profile` has no defined keys, the desktop never calls
the recipe endpoint, and nothing can create a recipe except direct SQL. The current
state is plumbing without a policy, which is the worst combination: it invites an
ad-hoc answer at the moment someone wants to play a DRM'd game.

There is a live precedent for where the ad-hoc answer leads. Optima, the reference
Ubisoft client, originally launched games by replacing Ubisoft's loader DLLs with its
own compatibility shims, and later removed that entirely. Its stated reasons are that
the shim answers ownership locally, so refunds and revoked licences stop being seen,
and that it was a hack. The removal is the position this record adopts.

## Decision

**Never circumvent protection.** No replacing or stubbing a provider's loader DLLs, no
ticket replay or forgery, no DRM shims, no emulation of a store's ownership answer.
What GameDepot does instead is environment correctness: give the depot copy an
environment that looks and registers like a real install, so the provider's own check
passes on its own terms. Where a title still refuses to run, that is a recorded
outcome, not an obstacle to engineer around.

Provider loader files that shipped inside the game travel as ordinary game files,
verified and installed like every other byte. We never author, download or ship such a
file ourselves, and we never ship a third-party substitute.

### Two postures, recorded per release

| | `direct` | `store_client` |
| --- | --- | --- |
| Who answers entitlement | nobody has to | the provider's own client |
| Typical | GOG and other DRM-free | Ubisoft Connect, EA app, Rockstar |
| Started by | us, natively or via umu | the client, which starts the game |
| Needs | prefix and runner | client installed and signed in, install registered |

Every release declares one. Ingested Ubisoft, EA and Rockstar titles default to
`store_client` with `compatibility = untested`. A posture is never guessed optimistically
at launch time, because a wrong guess looks like a broken game rather than a wrong
setting.

### The recipe profile gets a schema

`profile` becomes defined rather than free-form:

```json
{
  "launch": {
    "mode": "direct | store_client",
    "store": "uplay | origin | rockstar | gog | egs",
    "handler": "uplay://launch/{app}/0",
    "requires": ["client_installed", "client_signed_in"]
  },
  "runner": { "kind": "native | umu", "build": "GE-Proton9-27" },
  "env": { "PROTON_EAX_ENABLED": "1" }
}
```

`source` distinguishes `curated` from `observed`. `compatibility` is the
operator-facing truth: `untested`, `works`, `works_with_tweaks`, `broken`. It is set
from a real attempt on a real prefix, never by inference from a title's reputation.

### Registration is derived, not imported

`store_client` usually needs the client to believe the game is installed: a state record
plus the registry keys an installer would have written. Public dumps of Ubisoft's
install state exist, and ADR-0011 rules them out: unlicensed, and reconstructed from a
closed-source binary. Any layout we seed is therefore derived by inspecting an install
made on an operator's own machine, or observed on the wire during our own legitimate
acquisition, and the derivation is recorded beside the code that writes it. If we cannot
derive it, we do not offer handoff for that provider.

### Handoff has to own its bookkeeping

Under `store_client` our child process is the storefront, not the game. Play must follow
the game process for both Stop and playtime, or Stop kills the client while the game
keeps running and playtime measures the wrong thing entirely. `spawn_detached` returning
one pid is not sufficient here, and an unattributed session is worse than an honest "I
cannot track this title".

## Consequences

- Sign-in stays a human step. We surface "the store client needs a signed-in session in
  this prefix" and link out to it; we do not automate credential entry or 2FA, which
  keeps a provider password off the node entirely.
- Expect per-title variance, which is what the recipe is for. That is the argument
  against one global toggle and for the `(game, version)` row that already exists.
- Some titles will not work, and the posture model makes that visible as `broken`
  instead of leaving it as a support mystery. A tree with Denuvo and an always-online
  entitlement check may never be startable from a depot copy alone, and the answer is to
  say so.
- Sequencing: build this after a real GOG ingest, and implement handoff by taking one
  Ubisoft title with a real account and recording what it actually needed. Generalise at
  two data points, not zero. The `store` value in `umu_launch_spec` is already plumbed,
  so the missing work is registration, posture and process attribution rather than new
  plumbing.
- Two of the three gaps become explicit work items instead of latent confusion: the
  profile needs a schema and an admin write path, and the desktop needs to read the
  recipe it currently ignores.

## Alternatives considered

- **Loader shims, as Optima first did.** Rejected outright: it answers entitlement
  locally, which defeats refunds and revocation, breaks provider terms, and is exactly
  the thing this project's AGPL and provenance posture is arranged to avoid.
- **Refuse to ship anything DRM-gated.** Cleanest, and considered seriously. Rejected
  because these files were lawfully acquired by the operator, and DRM-free is a property
  of each title rather than of a library: the honest version is a recorded `broken`, not
  a policy that pretends the category does not exist.
- **Infer posture at launch** from detected DLLs in the tree. Rejected: a guess made in
  the moment of failure is undebuggable, whereas a declared posture plus an attempted
  launch produces a log that says what was tried.
- **Take the install-state layout from UplayDB's dumps.** Rejected on ADR-0011's
  grounds: derive it or skip handoff for that provider.

## References

- ADR-0010 (GOG platform handling), ADR-0011 (Ubisoft ingestion and which reference
  sources may be used).
- `desktop/src-tauri/src/{launch,runner,prefix}.rs`,
  `backend/internal/server/recipes.go`,
  `backend/sql/migrations/00006_runner_recipes.sql`.
- `docs/CLIENT-API.md` for the recipe endpoint contract.
- `Starkka15/Optima` README, for the shim-then-remove history. Cited as evidence; it is
  a GPL-3.0 project and is not a source of code (ADR-0011).
