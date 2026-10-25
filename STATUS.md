# Status
_Last updated: 2026-10-25, v0.2.0 released; releases now fire on a VERSION bump and docker-build.yml is gone._

## Current state
`GET /api/glance` is live on main: a single verdict for a panel, served from
the in-memory 60s window with no database read. `internal/health` is the one
evaluator behind it, and the dashboard reads the same verdict rather than
grading path health in its own renderer.

v0.2.0 is published: two tarballs with a `SHA256SUMS`, a multi-arch image on
ghcr, and the tag `v0.2.0`.

Nothing deploys on merge. Pulling a version tag is the only way code reaches
a machine.

## Releases
`VERSION` is the only source of truth. A chain of pull requests builds
nothing; the last one bumps `VERSION`, and that push to main runs the
**Release** workflow: frontend, `linux/amd64` and `linux/arm64` binaries,
tarballs with checksums, the pingway and pingway-reflector images, then the
tag `v$VERSION` and a GitHub release.

It refuses if `v$VERSION` or a bare `$VERSION` tag already exists, so a
reverted or cosmetic edit to the file fires the workflow and is turned away
rather than republishing a version.

The release is created last and makes the tag itself, so a tag never exists
for a release that did not happen.

## Install paths
Three, and they are not interchangeable. `install.sh` pulls the published
image and runs it under Docker with restart-on-boot. `run.sh` builds a local
image from the checkout. `install-binary.sh` downloads a release tarball,
verifies it against `SHA256SUMS` and runs the binary with no Docker and no
Go, taking its version from `VERSION` inside a checkout and from the latest
release otherwise.

Only the Docker paths survive a reboot. The binary path has no unit file.

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
- **One writer per image tag.** `docker-build.yml` and `release.yml` both
  tagged from `VERSION`, so a push to `deploy` could republish an existing
  version pointing at a different tree. Releases own the tags now.
- **The internet number is the best anchor, not the average.** One target
  rate-limiting ICMP should not paint the whole internet red, and loss counts
  only when every path is losing.

## Known issues
- The thresholds have only ever met fixtures. Nothing here has been checked
  against a real degraded network, and the first genuine `degraded` is the
  one worth watching.
- `internal/api/server.go` is not gofmt-clean on main (the `Server` struct
  fields are misaligned since `onTargetsChanged` was added). Left alone so a
  feature diff does not carry a reformat.

## Open threads
- No systemd unit for the binary install path, so it does not survive a
  reboot the way the Docker paths do.
- The firmware panel reads `lan_loss_pct`, `jitter_ms` and `worst_hop` to say
  *where* it is broken; all three are in the endpoint.
