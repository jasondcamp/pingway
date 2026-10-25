# Status
_Last updated: 2026-10-25, the health evaluator landed; the glance endpoint and UI rewiring follow._

## Current state
`internal/health` is new and has no callers yet. It is the single evaluator
for "is the network ok, and whose fault is it": ordered, nearest-hop-first
rules over a rolling window, with the thresholds passed in as a value rather
than compiled in.

The rest of the app is unchanged. Nothing reads the evaluator, so merging
this alters no behaviour.

## Recent decisions
- **One evaluator, not one per consumer.** The dashboard graded path health
  inside its own renderer, which is how a LAN at 95ms and 39% loss rendered
  as no message at all. A verdict computed once and read by everyone cannot
  disagree with itself.
- **Nearest hop first.** A broken LAN is a broken LAN even when the internet
  beyond it is perfect. The old logic could only express LAN health as a
  modifier on internet health, so it had no way to say this.
- **Absolute LAN thresholds, not baseline multiples.** `> 3 x baseline`
  cannot say 95ms on a local gateway is wrong, because it only compares the
  LAN to its own history. A gateway that is always slow looked healthy.
- **`stale` outranks every other rule.** Silence is `unknown`, never `ok`. A
  panel showing green because the prober died is worse than a blank one.
- **The ISP tier is never blamed alone.** Those routers deprioritise and
  rate-limit ICMP, so a silent hop is normal while traffic through it is
  fine. It counts only when the internet beyond it also suffers.
- **Pointers for "not measured".** No successful probe is a different fact
  from 0ms, and plain float64s would quietly equate them.

## Known issues
- `localizeFault()` in `frontend/src/pathviz.ts` is still the old renderer
  logic and still calls a badly degraded LAN healthy. It is fixed in the
  next PR, not this one.
- `internal/api/server.go` is not gofmt-clean on main (the `Server` struct
  fields are misaligned since `onTargetsChanged` was added). Left alone so a
  feature diff does not carry a reformat.
- main is 10 commits ahead of the last deploy (`fb4c469e`, 88 days ago).

## Releases

`VERSION` is the only source of truth. Bump it in a pull request, merge, then
run the **Release** workflow by hand. It builds the frontend, cross-compiles
`linux/amd64` and `linux/arm64`, publishes two tarballs with a `SHA256SUMS`,
pushes a multi-arch image to ghcr, and creates the tag `v$VERSION` with a
GitHub release.

It refuses if `v$VERSION` or a bare `$VERSION` tag already exists, which
means it refuses today: VERSION says `0.1.0` and that tag exists from before
the `v` convention.

A release does not deploy. `docker-build.yml` ships on push to `deploy`, and
main is well ahead of it.

## Open threads
- Next PR: `GET /api/glance` serving the verdict from the in-memory 60s
  window, thresholds persisted in Settings with a UI, and the dashboard
  reading the verdict instead of deriving its own.
- `/api/glance` rather than `/api/summary`: that path is already a
  DB-backed historical report, documented in README and SPEC and consumed by
  the frontend.
- The firmware panel reads `lan_loss_pct`, `jitter_ms` and `worst_hop` to
  say *where* it is broken; those are in the endpoint PR.
