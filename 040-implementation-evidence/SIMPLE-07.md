# SIMPLE-07 — operational documentation and three-lane coexistence

**State: IN PROGRESS.** The lane runbook is complete and Dev-only restart
isolation passed on all three Ubuntu workers. Mac Dev isolation is **NOT RUN**
because a fresh unsuffixed PRD health preflight intermittently returned HTTP
503. No Mac Dev/PRD/QAS service was stopped by the blocked preflight. Do not
claim the whole SIMPLE-07 acceptance until the Mac gate is completed.

## Operator documentation

[Existing-host lanes](../docs/lanes.md) documents:

- the four existing machines, PRD/Dev/QAS service identities, configuration
  and state roots, sockets, direct ports, and shared SSH port;
- lane-specific CLI/config selection for direct and queued work;
- guarded Dev/QAS status, stop, start, install, and rollback commands;
- the exact accepted per-host candidate relationship and stable PRD boundary;
- application concurrency/log limits, measured resource limits, and current
  operational blockers.

The normal CLI and mailbox guides remain linked from the lane runbook.
PRD is deliberately not a `runner-lane` update target. Its current build is
retained, and an upgrade remains optional, separately approved work.

## Source and host revisions

The SIMPLE-07 documentation delivery is based on the completed SIMPLE-06
receipt commit `702f76691174463b6b3f5da97ed57c5c273018b9`. Before these
SIMPLE-07 changes, the Mac `dev` checkout, primary Gitea `origin/dev`,
automatic GitHub `github/dev`, and all three clean Ubuntu `dev` checkouts were
at that exact revision. The host actions used synchronized local checkouts;
no tracked source was edited or copied to an Ubuntu host. Remote commands used
these verified explicit administrative SSH identities:

| Host | Mac SSH profile and identity |
| --- | --- |
| `linux-host` | `remote-session-runner`, `/Users/tomasz.walczuk/.ssh/remote-session-runner` |
| `sandbox-host` | `sandbox.env`, `/Users/tomasz.walczuk/.ssh/dev.slidestud.io` |
| `factory-sandbox` | `factory-sandbox`, `/Users/tomasz.walczuk/.ssh/factory-sandbox` |

Runner was not used for `systemctl`, launchd, or local Unix-socket health
observations; those host-local operations used the already authorized Mac
shell/SSH fallback. The guard and restart command was
`go run ./src/cmd/runner-lane stop|start --host HOST --lane dev` from each
host's synchronized source checkout, with its installed Go 1.27.1 toolchain.
No direct `systemctl stop`, `launchctl bootout`, Runner request, or ACK was
used.

## Ubuntu Dev restart isolation

Each worker was checked immediately before its serial Dev-only restart. Dev
had `readiness=ready`, `sqlite_writes=ready`, zero active sessions/commands,
queues, intents, and mailbox backlog. Each restart used the guarded
`runner-lane stop` followed by `runner-lane start`; the first immediate Dev
socket check can precede listener readiness, so readiness was polled before
postflight. No new work was submitted and no retained request/ACK was removed.

| Host | Preflight / restart UTC | PRD identity stayed unchanged | QAS identity stayed unchanged | Dev postflight |
| --- | --- | --- | --- | --- |
| `linux-host` | 09:33:43–09:33:47 | PID `149913`, build `2802f00`; binary SHA `22f33d6cd3d429c6d91537031954222d7a648e3a3d46e1e169ddbeaa89c21104` | PID `2892643`, build `2204e18`; `runnerd` SHA `acbc42bfc9ce496b49097e98d6de53b02f5a3ac47a56410686341ef3807e14a7` | PID changed from `2705996` to `2996759`; ready at `2204e18`; zero work; Dev `runnerd` SHA remained `acbc42bfc9ce496b49097e98d6de53b02f5a3ac47a56410686341ef3807e14a7` |
| `sandbox-host` | 09:34:53–09:34:57 | PID `717823`, build `2802f00`; binary SHA `b0d864b1ff285f6591868aa463e913537955bf1a0a06130f31e8fdd635004491` | PID `1246561`, build `2204e18`; `runnerd` SHA `5abbb5d8bcde3f11c3800a99fc1860b489e1ef3ac233c974d16a2178bda56192` | PID changed from `1015717` to `1370403`; ready at `2204e18`; zero work; Dev `runnerd` SHA remained `5abbb5d8bcde3f11c3800a99fc1860b489e1ef3ac233c974d16a2178bda56192` |
| `factory-sandbox` | 09:35:46–09:35:56 | PID `50302`, build `2802f00`; binary remained unchanged | PID `1352979`, build `2204e18`; `runnerd` SHA `acbc42bfc9ce496b49097e98d6de53b02f5a3ac47a56410686341ef3807e14a7` | `runnerd-dev.service` stopped/started through the guard; ready at `2204e18`; zero work; PRD/QAS systemd active state, PID, start time, and binary hash comparisons passed |

All three workers' PRD, Dev, and QAS health endpoints returned `ready` after
their restarts; PRD retained work was preserved (sandbox's PRD session slot
remained 1). QAS services remained active on their accepted bundles. A
postflight shell assertion initially failed because `systemctl show` emitted
properties in a different order than assumed. The corrected read-only check
compared `ActiveState`, `MainPID`, and `ExecMainStartTimestamp` by property,
then compared binary SHA-256 values; it passed on sandbox and factory. No
service operation was repeated to compensate for that assertion issue.

## Mac preflight blocker

The latest Mac attempt at `2026-10-18T09:40:09Z` ran no restart: its required
fresh health preflight exited before returning a report. Earlier explicit
probes showed unsuffixed `runner-locald` alternated between HTTP 503 and HTTP
200. Ten consecutive `runner-locald` responses at `09:30:11Z`–`09:30:39Z`
returned `readiness=ready` and `sqlite_writes=ready`, but cumulative locald
`storage_errors_total` increased from 598 to 599, and later observations
reached 602. The Mac API and locald continue to retain exactly one session
slot, zero active commands, queues, intents, and mailbox backlog. The last
confirmed PRD process PIDs are `48708` (`runner-local`) and `48627`
(`runner-locald`); no PRD restart was attempted.

Read-only owner-shell checks still show the PRD SQLite database passes
`PRAGMA quick_check`, is in WAL mode, and is owned by the service account with
the expected `0700` parent and `0600` DB/WAL/SHM modes. Disk space is not
constrained on the Mac. A fresh `ps`/`lsof` process inspection showed only
those two PRD service processes holding the DB/WAL/SHM read-write. Service
stderr continues to report only the sanitized queue-worker reason
`operation_failed`; it does not expose the SQLite error. The owner attempted
the bounded root `fs_usage` trace, but macOS paused at its administrator
confirmation. The following shell lines were entered while that confirmation
was pending, the background trace stopped, and no filtered trace output was
captured; `rg` was also unavailable in that shell. The four readiness GETs
were subsequently run without an active trace, so they do not provide the
missing OS error. No data, permissions, configuration, binaries, service,
mailbox, held ACK, or retained reservation was changed. To finish diagnosis,
authorize `sudo -v` in Mac Terminal first, wait for it to return to the shell,
then run a fresh bounded trace against the current `runner-locald` PID and
provide the filtered SQLite/errno lines. Until that diagnosis and fresh stable
PRD postflight, Mac Dev restart isolation remains blocked.

## Measured resources and remaining limits

Filesystem and memory were refreshed read-only at `2026-10-18T09:50:14Z`
after QAS installation. Available space and memory change over time and must
be rechecked before builds or updates. The systemd limit values were inspected
during the preceding SIMPLE-07 host checks; the table separates these service
limits from the timestamped resource snapshot.

| Host | Existing filesystem available | RAM snapshot | Service-level resource enforcement |
| --- | ---: | ---: | --- |
| Mac | 160,398,760 KiB available on `/System/Volumes/Data` (about 152.9 GiB) | 48 GiB physical; memory pressure reported 41% free | No lane CPU/RAM reservation; ordinary existing account and filesystem. |
| `linux-host` | 19,907,736 KiB (about 19.0 GiB) | 952 MiB total; 462 MiB available; 1 GiB swap | `CPUQuota`, `MemoryHigh`, `MemoryMax` unlimited; `TasksMax=1035` per service. |
| `sandbox-host` | 1,629,620 KiB (about 1.55 GiB); filesystem 97% used | 5.77 GiB total; 4.38 GiB available; 8 GiB swap | `CPUQuota`, `MemoryHigh`, `MemoryMax` unlimited; `TasksMax=7000` per service. Recheck capacity before any write/build; do not remove retained work or shared caches. |
| `factory-sandbox` | 43,674,056 KiB (about 41.7 GiB) | 951 MiB total; 338 MiB available; no swap | `CPUQuota`, `MemoryHigh`, `MemoryMax` unlimited; `TasksMax=396` per service. |

The application caps each Dev/QAS lane at two sessions and one running
command, with 16 MiB component logs and four retained files. These do not cap
CPU, memory, or total disk use and do not isolate hostile processes. The
sandbox's 1.56 GiB free and the low available RAM on Linux-host/factory-sandbox
are the measured operating limits; no extra storage or hardware is authorized.

## Closeout decision

SIMPLE-07 documentation is complete and Ubuntu coexistence checks pass.
Overall SIMPLE-07 remains **IN PROGRESS** until Mac PRD returns a fresh
successful preflight and the Mac Dev-only restart proves PRD/QAS identities
unchanged. Preserve SIMPLE-04/SIMPLE-05 results and all held ACKs while that
single host gate is pending.

## Documentation delivery receipt

The runbook, evidence, navigation links, and in-progress ledger status were
committed on Mac `dev` as `5bb0d8dc5b83b86611aaf7124c24e25dd79b0303`. It was
pushed to primary Gitea `origin/dev`; direct `ls-remote` reads returned that
same SHA from Gitea and automatic GitHub `github/dev`. Before pulling, all
three Ubuntu checkouts were clean on `dev` at
`702f76691174463b6b3f5da97ed57c5c273018b9`. Each fast-forwarded
`origin/dev` using its explicit GitHub identity. Final read-back confirmed
clean `dev` checkouts at `5bb0d8dc5b83b86611aaf7124c24e25dd79b0303` on
`linux-host`, `sandbox-host`, and `factory-sandbox`, with `docs/lanes.md`
present. `git diff --check` and changed-Markdown relative-link checks passed.
No runtime test or service change was required for this documentation commit;
the recorded Ubuntu restart-isolation acceptance remains the separate live
evidence above.
