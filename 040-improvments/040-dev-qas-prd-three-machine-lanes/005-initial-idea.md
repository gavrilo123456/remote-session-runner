# Initial idea: Dev, QAS, and PRD release lanes

**Status:** reviewed implementation direction. The detailed design and phased
plan define the isolation, promotion, capacity, and migration gates. No lane
service, mailbox, credential, firewall, or deployment has changed yet.

## Problem

Runner currently supports several mailboxes and remote hosts from one Mac
installation, but it is still one installed code revision and one runtime
instance. Naming an execution environment `dev`, `qas`, or `prd` would label a
request, but would not isolate its binary, SQLite state, sockets, workspaces,
logs, service labels, credentials, endpoint, or resource budget.

The required workflow needs three versions to run at the same time:

- **Dev** changes and tests Runner behavior.
- **QAS** independently tests the selected candidate release.
- **PRD** continues to serve stable mailbox producers while Dev and QAS change.

A branch-only split or a shared Runner database would allow one lane to affect
another and does not meet that requirement.

## Goal and host scope

Introduce three fixed, concurrently runnable **release lanes**: `dev`, `qas`,
and `prd`. Each lane spans the Mac control plane and all currently accepted
Ubuntu profiles: `linux-host`, `sandbox-host`, and `factory-sandbox`.

The directory name "three-machine lanes" predates `factory-sandbox`. The actual
cohort is four physical machines: one Mac and three Ubuntu hosts. A remote
profile is not silently excluded from a lane; its capacity, direct mTLS,
queued-bridge, mailbox, and live-acceptance evidence are all required.

`lane` is a deployment/release identity. It is deliberately separate from the
existing execution-policy labels `linux-dev`, `sandbox-dev`, and
`factory-sandbox-dev`, which describe targets in the current V2 configuration
rather than a Dev release lane.

## Settled naming and compatibility rule

`prd` is a release identity, not a suffix on production resources. The future
PRD lane keeps existing unsuffixed names, paths, listener, and mailbox roots.
Dev and QAS receive a suffix whenever a resource lives in a shared namespace.
A file contained only under a suffixed service root can retain its normal leaf
name because its full path remains unique.

| Resource | Dev | QAS | PRD compatibility mapping |
| --- | --- | --- | --- |
| Mac service root | `RemoteSessionRunner-dev` | `RemoteSessionRunner-qas` | `RemoteSessionRunner` |
| Mac LaunchAgent labels | `com.remote-session-runner.local-dev`; `com.remote-session-runner.locald-dev` | `com.remote-session-runner.local-qas`; `com.remote-session-runner.locald-qas` | existing unsuffixed labels |
| Linux unit | `runnerd-dev.service` | `runnerd-qas.service` | `runnerd.service` |
| Linux service root | `remote-session-runner-dev` | `remote-session-runner-qas` | `remote-session-runner` |
| Direct HTTPS port on every Ubuntu host | `8444` | `8445` | `8443` |

The current unsuffixed runtime is a PRD compatibility baseline, not an already
migrated PRD lane. It becomes explicit `release_lane: prd` only in the
scheduled PRD migration. Dev and QAS are fresh installations and must not
rename, move, stop, repoint, or share runtime state with the baseline.

## Configuration, mailboxes, and routes

Every installed lane has an independent owner-only V3 configuration with a
required `release_lane`, service root, SQLite authority, sockets, mailboxes,
logs, workspaces, temporary files, backups, binaries, endpoints, mTLS
materials, and queued-SSH materials. V2 stays intact while Dev and QAS are
introduced; V3 does not silently reinterpret V2 as PRD.

An external mailbox belongs to one lane globally. Existing unsuffixed roots
become PRD-only during PRD migration. Dev and QAS use fresh lane-named roots,
for example reviewed `mailbox-dev` and `mailbox-qas` siblings. A Mac-wide
owner-only lane registry prevents any two independent lane databases from
claiming the same normalized root.

A publisher selects the lane by selecting its configured mailbox root or local
endpoint before publication. A mailbox request may select only the complete
configured environment/target pair that its inbox permits. It cannot provide a
lane or arbitrary host, account, port, certificate, bridge, or filesystem path.
Direct mTLS and queued SSH use separate lane-specific identities and fixed
targets.

## Promotion model

Tracked source changes are made only in the authoritative Mac `dev` checkout.
The existing Gitea-primary → GitHub-mirror → clean Ubuntu `dev` checkout flow
remains the source-delivery path.

QAS and PRD are not source-edit branches or deployment worktrees. A candidate
is an immutable build cohort from a recorded Dev source SHA plus per-host
artifacts. The same recorded artifact bytes are installed from Dev to QAS to
PRD on each physical host; platform and host-specific binary digests remain
distinct.

```text
clean dev SHA → candidate manifest → Dev authorization/acceptance
→ QAS authorization/acceptance → PRD authorization/acceptance
```

A QAS failure leaves PRD unchanged. Correction returns to `dev` as a new commit
and candidate manifest.

## Practical boundary

The lanes provide operational separation against configuration, installation,
transport, and ordinary resource-management mistakes. They are not a
hostile-code boundary while arbitrary scripts in all lanes run as the same Mac
or Ubuntu user. Strong containment against Dev code requires separate OS
accounts or a VM/sandbox. Linux has hard resource reservations. On the shared
Mac, Dev/QAS are remote-execution control planes: quota-limited APFS storage,
byte-based admission, bounded caches/logs, host headroom, and background
priority limit ordinary storage failures, but do not promise a per-lane CPU/RAM
reservation. The first rollout permits Dev/QAS submitted scripts only on remote
targets; a local Dev/QAS execution target waits for a separately sized VM or
host.

## Acceptance direction

Each lane must prove its Mac service and every remote profile separately. The
proof includes lane/revision/artifact/configuration attestation, positive
queued and direct mTLS routes, a native mailbox request with events and ACK,
zero-work postflight, denial against every foreign lane available in that
deployment phase, queued-wrapper pinning, and proof that Dev/QAS refreshes
leave PRD configuration, state, Linux reservation, and Mac storage safety
unchanged. Mac control-plane compute isolation is explicitly not guaranteed in
the shared-user first rollout.

The complete implementation and migration rules are in the
[detailed design](010-dev-qas-prd-three-machine-lanes-detailed-design.md) and
[phased implementation plan](015-dev-qas-prd-three-machine-lanes-phased-implementation-plan.md).
