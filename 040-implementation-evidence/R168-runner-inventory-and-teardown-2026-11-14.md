# R168 Runner inventory and completed-command teardown

## Scope and exact exchange

Owner requested supported inventory restoration and disposition of only:

- Request `req-asf-p09-r168-nacc-record-supervised-20261114-agent-001`.
- Command `cmd-877df533a8e0a90b0b8c38fa4bc740a8`.
- Session `sess-e5d66620e61ddaa66f92968828390803`.
- Job `job-bc3fe465a2317ee53671f2a186beb57e`.

Owning authority is Mac PRD, UID 502, policy `mac-dev` and local profile
`mac-workstation`. The selected and running service revision is
`0b7c031d7025b79e712bc52e73367a41bdefcf4e`, release
`rr-20261111-bug031-0b7c031`. No service, selector, configuration, database,
capacity, request or ACK has been changed by this investigation.

## Original evidence preserved

Supported command/job reads confirm failed exit 1, complete untruncated output,
event 8 `command_failed`, job phase LOST and `runtime_cleanup_unconfirmed`.
The original outbox remains revision 4/event 8. Its supported mailbox lifecycle
is `terminal_unacknowledged`; no ACK pair exists.

- Original response SHA-256: `24c42df8bd239925aec55d9ef95e0673610291a1b7b746a9fafb5b8fe0a1f6ed`.
- Original eight-event prefix SHA-256: `79f42256649d48d70044311304403d81445990cd538dd758d5b364a3fa8dfee3`.

Private captures and the full metadata inventory are under
`/private/tmp/rsr-mac-exact-teardown-20261114-url98z29/`.
Scripts and command output are not included in this tracked report.

## Inventory observations and safe diagnostic correction

The installed owner-only diagnostic is source
`883e2af0373b7de94f172df31909434f9a23c38d`, binary SHA-256
`6636a43c0b2eff4013a8696d4e3fec3d3cfd83ec1dfd0df47c0501b227e0d196`.
Its direct supported read succeeded at 18:58:36–18:58:38 UTC on 2026-11-14,
exit 0 and `inventory_complete=true`, schema 40. It reported nine retained
sessions/reservations, one held command slot, no recovery finalizations,
5,552 local intent rows and 25 remote intent rows. Intent delivery records are
not evidence of currently executing local commands.

All DB/WAL/SHM identity guards passed. The earlier Factory failures are not
reproduced by this successful cut. Their generic errors do not establish a
cause; duration alone is insufficient to call them timeouts. The independently
identified diagnostic defect is missing safe boundary/category information,
tracked as [BUG-032](../050-bugs/032-retained-work-inventory-failure-omits-safe-boundary.md).
The diagnostic correction is source `cedf7100bcad20f84ab1e085f686450ce15b7720`.

Mac qualification for this diagnostic correction passed using Go 1.27.1 and
the shared Go/module caches: the focused inventory/BUG-027 regressions and
native vet passed; complete changed-package tests passed for `laneinstall`
(50.593 seconds) and `runner-lane` (4.957 seconds).

All three clean Ubuntu `dev` checkouts pulled the exact Gitea/GitHub source.
Go 1.27.1, native CGO-enabled changed-package tests, vet and attested
`runner-lane` builds passed on each host:

| Host | Platform | laneinstall tests | runner-lane tests |
| --- | --- | --- | --- |
| Linux | linux/amd64 | 47.908 s | 14.210 s |
| Sandbox | linux/arm64 | 90.677 s | 7.651 s |
| Factory | linux/amd64 | 39.884 s | 12.395 s |

The separately built Mac diagnostic SHA-256 is
`3f151e1f59cd89cd63298d3a3933f966e7c8e01f1eddecf98b4ce1a96358e4c8`.
It was installed at the documented owner-private `bin/runner-lane` path,
mode `0700`, preserving the original executable in the private evidence
directory. No selected service binary was rebuilt or replaced.

At 19:15:05 UTC, the installed corrected tool returned exit 0 with complete
schema-40 inventory in 4,425 ms. The target reservation remained held, its
command slot remained released, and all eight older retained-pair rows matched
the initial baseline. Original response/events hashes and absent ACK files
were verified unchanged. `safe_to_resume=not_established` and
`recovery_authorized=false` remained intact. Private qualification, install
and postflight manifests accompany this report. This operator acceptance
does not establish a cause for the two earlier generic failures.

## Teardown cause and current process observation

The installed executor recorded exactly one sanitized cleanup diagnostic at
18:18:35 UTC: `failure_stage=group_wait`,
`error_category=group_still_runnable`. The 500 ms grace ended while one
non-zombie descendant remained in the exact process group. This is a confirmed
live-member refusal, not evidence of the older zombie-only failure.

Recorded root: PID/PGID 99768, start identity `1794680102686463`.
Recorded sole member: PID 1558, PPID 1, UID 502, process group 99768,
start identity `1794680222251393`. Elapsed observation was 511 ms, with
one runnable and zero zombie members.

A read-only process observation at 19:02:12 UTC found no rows matching those
PIDs or PGID. That observation does not complete supported teardown, release
capacity or establish Factory administrative-operation outcomes.

## Exact supported disposition: still blocked

The target is classified `completed_teardown`: its command slot is already
released, but its session reservation remains unreleased with no cleanup proof.
Use neither lost-command recovery nor a replay/ACK to change that fact.

Eight unrelated retained reservations must be preserved: seven older completed
teardowns and one older lost-command pair with a held command slot. The selected
executor and catalog wrapper require a complete idle retained set, zero live
command slots and no unselected held reservations for completed-teardown
recovery. Consequently an exact one-pair invocation is known to refuse.
No service was stopped merely to reach that refusal; no broader recovery was run.

The present supported disposition is **STILL BLOCKED; reservation retained**.
Fresh health is ready, but readiness and the absent observed group do not clear
this exact reservation or authorize Factory resumption. Resolving only this
pair while preserving older holds needs a separately qualified exact-subset
recovery capability; a broader retained-set cleanup needs its own reviewed
authorization. The owner confirmed PRD publishers/clients paused and chose
**keep the exact scope; prepare a scoped recovery correction**. This authorizes
source preparation for that separate capability; it does not authorize
recovering any of the eight older holds or treating this pair as cleared.
