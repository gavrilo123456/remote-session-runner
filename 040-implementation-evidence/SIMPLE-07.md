# SIMPLE-07 — operational documentation and three-lane coexistence

**State: PASS.** The lane runbook is complete, and guarded Dev-only restart
isolation passed on the Mac and all three Ubuntu workers. Mac PRD and QAS
service identities and installed bytes remained unchanged. The recurring Mac
PRD queue-claim warning still has no captured raw SQLite/OS cause; fresh PRD
readiness passes and no production mutation was justified. One PRD mailbox
backlog item and queued intent appeared during postflight; they were preserved
without inspection or disposition.

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

Runner was not used for service-manager or local Unix-socket health
observations; those host-local operations used the already authorized Mac
shell/SSH fallback. The guard and restart command was
`go run ./src/cmd/runner-lane stop|start --host HOST --lane dev` from each
host's synchronized source checkout, with its installed Go 1.27.1 toolchain.
No direct/manual `systemctl` or `launchctl` service command, Runner request,
or ACK was used. The runner-lane guard invoked the selected service manager.
The Mac Dev-only restart used the synchronized Mac checkout at
`748a1944df7fe44d2fab5279cd610cc25befa574`; changes since the Ubuntu restart
source were documentation/evidence only and did not modify runner-lane.

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

## Mac Dev restart isolation and PRD diagnostic limit

The earlier `2026-10-18T09:40:09Z` Mac preflight returned no report and did
not restart a service. Prior probes showed PRD `runner-locald` intermittently
returned HTTP 503 and HTTP 200; ten checks from `09:30:11Z`–`09:30:39Z` were
ready, while its cumulative `storage_errors_total` later reached 602. A
bounded root `fs_usage` trace was attempted by the owner, but macOS paused at
the administrator confirmation. The next shell lines were entered at that
prompt, the trace job stopped, and no filtered output was captured; `rg` was
not installed. Four health GETs then ran without an active trace. Thus the raw
SQLite/OS cause remains unknown and no corrective PRD file or service change
is claimed.

Fresh preflight at `2026-10-18T09:58:55.587Z` (PRD API) and
`09:59:02.357Z` (PRD locald) returned HTTP 200 and `readiness=ready` on the
original build. Each retained its pre-existing one-session slot with no
active commands or mailbox backlog; API/locald reported zero queued intents.
The locald cumulative storage-error counter was 615 and cleanup failures 1.
At `10:00:18Z`, Mac Dev and QAS API/locald also returned HTTP 200 and ready,
each with zero active sessions/commands, queues, intents, and mailbox backlog.
`runner-lane status --host mac-control-plane --lane dev` reported active.

The default sandbox denied the first guarded Dev stop at `launchctl bootout`
with `Operation not permitted`. Immediate read-only rechecks confirmed both
Dev agents remained running (PIDs `61821` and `61872`), and Dev locald stayed
ready with zero work. The same guarded Dev-only stop was then run through the
authorized elevated Mac shell and completed; `runner-lane status` confirmed
Dev stopped before the selected-lane start. No direct launchctl command was
used. The Dev-only start completed, and final `runner-lane status` reported
active.

Postflight at `2026-10-18T10:03:11Z`–`10:03:16Z` returned HTTP 200 and ready
for all six Mac API/locald endpoints. Dev and QAS remained at build
`2204e18ecd6eddc916ffa39e75ac905d3dbe2851` with zero active work. PRD remained
at build `77910358a149d6e8fcdf4694498ebdf6c1f07f58`; its existing session slot
remained 1. The identity and byte comparisons were:

| Lane | Before | After | Installed component SHA-256 after |
| --- | --- | --- | --- |
| PRD | `runner-local` PID `48708`; `runner-locald` PID `48627` | Same PIDs | `runner-local` `b632233613858ad2541dfd7d18915eb8924df3abe082926b6ece0a1df6b66455`; `runner-locald` `e7f15eea593c402fb2b1e7605aafc9d558e3289222d54abade56b8bacb60ec13` |
| Dev | LaunchAgent PIDs `61821` / `61872` | PIDs `8483` / `8578` | `runner-local` `f860c21c5009447ee18d224a4d024ab785f76d5c717290f17cc3c1a5b9fe8049`; `runner-locald` `d1dab133673eebceb8743795c7033333b2c4197f15c6000958bf5935e62d0040` |
| QAS | LaunchAgent PIDs `11302` / `11369` | Same PIDs | `runner-local` `f860c21c5009447ee18d224a4d024ab785f76d5c717290f17cc3c1a5b9fe8049`; `runner-locald` `d1dab133673eebceb8743795c7033333b2c4197f15c6000958bf5935e62d0040` |

The Dev and QAS `current` symlinks remained on bundle
`0cbaff313de36ebe030144dd4192a1138aebb4b0ad54ca6fe93d6d0ff196f757`; their
manifest SHA remained
`88e82159e2719bb510fe306b540464c94270866cb3fc120e696f0360845b617a`.
PRD's binaries, configuration, database/WAL/SHM, and service identities were
not changed. No Runner request or ACK was published by this restart. Postflight
did show a PRD API mailbox backlog of 1 in
`agentic-software-factory-runner-direct` and locald `queued_intents=1` /
`mailbox_backlog=1`, where preflight had reported zero. The cause was not
established; the item and retained session were left untouched. PRD
`storage_errors_total` was 617 postflight; the recurring generic
`operation_failed` cause still needs the root trace and is not claimed fixed.
To retry the bounded diagnosis, the owner must first run `sudo -v` in Mac
Terminal, accept the administrator confirmation, and wait for the shell prompt
to return before pasting the separate trace commands. Use `/usr/bin/grep` for
filtering; `rg` was not installed in the previous shell. This trace is not a
SIMPLE-07 restart-isolation prerequisite.

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
sandbox's 1.55 GiB free and the low available RAM on Linux-host/factory-sandbox
are the measured operating limits; no extra storage or hardware is authorized.

## Closeout decision

SIMPLE-07 is **PASS**: the operational runbook is delivered, the Dev-only
restart passed its work guard and postflight on the Mac and all three Ubuntu
workers, and PRD/QAS service identities and installed bytes remained unchanged
on each host. The Mac PRD's intermittent sanitized queue-claim error remains
an unresolved diagnostic limitation, but fresh readiness passed and no
unsupported production mutation was made. A one-item PRD backlog/intent seen
after the Mac restart was preserved without inspection, ACK, replay, or drain.
The stable production binaries and retained session remain in place.

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
