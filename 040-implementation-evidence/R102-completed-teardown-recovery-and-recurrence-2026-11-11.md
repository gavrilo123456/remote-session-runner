# R102 — Mac completed-command teardown recovery and recurrence investigation

**Runner recovery COMPLETE; affected Mac route cleared for fresh Runner work.**
The original failed qualification and unACKed result remain unchanged. Factory
eligibility, reservations and historical #005 clearance are separate.

## Exact scope and preserved result

Mac `mac-control-plane`, `tomasz.walczuk` / UID502. Actual Runner namespace
PRD, configured Factory policy `mac-dev / local/mac-workstation`.

- Request `req-asf-p09-user33-r102-bounded-codex-qualification-20261111-agent-001`
- Session `sess-57e46c20f902018ca50826a15c26e675`
- Command `cmd-976dcba880a6a1c5b7d8dbe9b59d3942`
- Job `job-740086d9b5ec8dce4cfe8071c7507e98`
- Preserved command FAILED, exit2, revision3, final/available cursor9,
  complete output, not truncated. Original job/session teardown remains LOST
  with `runtime_cleanup_unconfirmed`; a later cleanup proof does not rewrite
  the historical outcome.
- Supported lifecycle still `terminal_unacknowledged`; no ACK was published.
- Original response SHA256 `b23a51449eda1bc52061f45b2d7895e4983a653a14603d50c893297e205be1c7`.
- Original event stream SHA256 `b484a7326dbf81eb7681a96040ebabda76b6e4f461c01b221e9c84368f87f180`.
- Factory terminal archive SHA256 `72351cf79db8fe43be86455b0f94555ced6123d58cef0e8c3670dea445f782a5`.

The owner authorized exact recovery and confirmed current publishers paused.
The handoff's old self-observed inventory was not reused as an idle cut. Fresh
independent complete inventories identified only this completed teardown,
zero command slots, one retained session reservation and no other active,
queued, resumable or pending-finalization work. Dev/QAS were independently
inventoried and preserved.

## Ownership and supported maintenance

The owner marker bound PID/PGID **51307**, UID502, start identity
**1794356099484423**, runtime generation
`9cf47fb9a064e8be3d766711322050bf`, and owned Runner workspace
`session-2771527186`. Independent native observations at
**2026-11-11T06:18:53 UTC** found the root absent with ESRCH before and after
the observation and zero members of its process group. Boot session remained
`012036E7-D160-4A1A-B4C2-8656A146E61C`; the Mac had not rebooted since the
incident. Native recovery independently revalidated cleanup under its lifecycle
lock; no operator-issued process signal was needed.

The exact previously qualified controller `fe807c65a3fc3fbfa5eb041dfbf180c6f89b19ef`
was reverified by SHA256
`d5f201d91fa348c9ffebbf11d39c142a72b7b2e47c44875630ae8e3144efcdfb`.
Its recorded Mac/three-worker qualification was reused for unchanged bytes;
there was no new controller or service build for this recovery.

Supported sequence: fresh `catalog-prd-backup` → `prd-stop` →
`catalog-prd-recover --teardown-pair` for this complete exact set → nested
stopped zero-work/finalization postflight → `prd-start` → live inventory,
readiness, original-result and mailbox-byte/ACK checks. Recovery was invoked
once, returned zero and reported **one** recovered completed-teardown pair,
**zero** recovered lost-command pairs, verified ownership and unchanged selector.
All execution/capacity/finalization counts were zero before restart.

The backup is `/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/backups/prd-catalog-20261111T062001.802898000Z`.
Backup manifest SHA256 `809649d24beef01279b811e7fd42fd9006d2fa0c58affaff1eec68570b4a8fb1`.
The supported stop completed at **06:20:06.190950 UTC**; restart completed
**06:20:28.505334 UTC**. Both Mac PRD services and all six Mac lane ingress /
executor readiness endpoints subsequently returned HTTP200 and `ready`.

Selected release stayed `rr-20261108-bug029-1263205`, source
`126320552acf0a2609d7d75c09016b867b1dbd13`, schema40:

- Release digest `1b54ce565da1c94d64604f8ce74462dd49974f67d60e0fcc8f772339e82f0603`.
- Mac bundle `25b8b596b00dc96933ee947a9180e3c024d9cfd469047d7eb0811fffb635b004`.
- Config `2b35d897a7df65b13e77fbcdc0749da6f7df8d654d16a535a33faac5afbf2eb2`.

Postflight found no relevant Runner sessions/commands/jobs, session reservations,
command slots, retained pairs or finalizations. The original owner marker and
owned workspace were finalized. Local/remote delivery-intent identities/states
were unchanged across maintenance; they are not execution counts. Dev/QAS
configuration, selector and database identities were unchanged. No Ubuntu
runtime service, Factory reservation/fence, selector, configuration, script
replay, qualification redispatch or ACK was changed. External publishers were
not resumed by the maintainer.

**Route disposition:** safe for fresh work at Runner's cleanup/capacity boundary.
This is an operator conclusion from exact recovery plus postflight; the generic
inventory's conservative `safe_to_resume=not_established` was not altered or
represented as clearance. Factory must apply its own separate eligibility rules.
This does not claim complete arbitrary detached-descendant coverage.

## Why R100 and R102 repeated: proven facts and limits

| Observation | R100 | R102 |
| --- | --- | --- |
| Ordinary failed result | exit2/event10 | exit2/event9 |
| Close authorization UTC | Nov10 21:46:25.361126 | Nov11 00:20:42.419780 |
| Terminal response observation UTC | Nov10 21:46:25.875443 | Nov11 00:20:42.943039 |
| Elapsed | **514.317 ms** | **523.259 ms** |
| Teardown/reservation | LOST / held | LOST / held |

These intervals strongly match `MacProcessAdapter.CleanupContext`'s fixed
**500ms** post-Bash process-group disappearance check. They do not identify
which members existed then. R100 and R102's later root/group absence cannot
prove earlier absence, a zombie, or a physical teardown fault.

A concrete **Mac cleanup false-retention defect** was independently reproduced:
`processGroupExists` uses `kill(-PGID,0)` and treats success or EPERM as present.
Darwin can report EPERM for a group containing only a zombie. Normal cleanup
therefore waits500ms and returns LOST despite no runnable group member. A
live redirected child also keeps the group present and must legitimately
retain capacity; these two cases must not be conflated.

The disposable real **MacProcessAdapter** Go-overlay test completed an ordinary
exit2 command, enumerated only a native Status5 zombie in the shell's group,
observed exact500ms cleanup refusal and retained marker, then allowed the
fixture parent to reap its own child. A second cleanup succeeded. The test
passed (package1.300s) on source `c4ef6cb6984c066e9426af0be4be8cc4d588e5b6`
using Go1.27.1. No tracked source or live service was changed for the fixture.
In the adapter result, UID0/PPID0 are inspector placeholders for a listed
unreaped PID whose proc_pidinfo returned ESRCH; they do not identify a real
root-owned fixture process. A separate exact fixture ps snapshot confirmed
`Z/<defunct>` and the retaining parent. Both fixture groups disappeared after
natural exit/reaping; no unrelated processes were signalled.

Reproducer packet: `/private/tmp/rsr-r102-cleanup-repro-verified-toqz3ked`.
Actual adapter result SHA256:
`0254c65630c613da430e21a21865e5f0cace58701f0e4d80b67212b4f01c6772`.
The packet includes the overlay test, bounded native observations, scripts,
helper exit evidence and hashes. This is native disposable regression evidence,
not a replay of either Factory qualification and not a full source suite.

A second confirmed defect prevents exact historical attribution:
`execution.Service.markClosingLost` increments cleanup failure and persists
only `runtime_cleanup_unconfirmed`. The queue worker's terminal-job branch
returns before its generic failure log. Neither the detailed cleanup error
nor a sanitized group-member snapshot is retained. Therefore the exact physical
trigger of R100/R102 remains **UNKNOWN**; zombie false retention is a proven
candidate, not an established historical diagnosis.

## Narrow preventive correction required

Track this investigation as [BUG-031](../050-bugs/031-mac-cleanup-zombie-groups-and-missing-teardown-cause.md).
The preventive service correction is **not implemented or deployed** by this
recovery/investigation. Installed PRD still contains the reproduced behavior.

1. Change only normal post-reap Mac cleanup to bounded native exact-group
   inspection: verified zombies are inert; any runnable, foreign, changed,
   truncated, uninspectable or unknown member remains blocking. Keep root
   incarnation, ownership and context guards. Do not change global EPERM to
   absence, weaken lost-recovery semantics, signal arbitrary descendants or
   claim confinement of separately-sessioned descendants.
2. Persist/log stable sanitized stage/category plus exact owner/session/group
   binding and bounded member evidence at cleanup refusal, including terminal
   jobs. Cover root wait, residual runnable group, zombie-only group, inspection
   failure, context cancellation, workspace and marker-finalization errors.
   Do not log scripts, argv, environment, output, secrets or unbounded ps data.
3. Add native adapter regressions for zombie-only, transient/live/mixed groups,
   denial/truncation/race, cancellation and retained evidence. Add terminal-job
   diagnostic tests. Preserve truthful failed result, event cursor, capacity
   release proof and ACK isolation.
4. Qualify any changed service bytes as a new release through normal
   Dev→QAS→PRD gates. Do not patch the installed sealed bundle or substitute
   an unqualified service to finish a recovery.

Extending the timeout alone is not a fix for an unreaped zombie. Recovery
success is separate from preventive correction and future qualification.

## Raw evidence

Owner-private packet: `/private/tmp/rsr-r102-teardown-20261111-bl5jk8f6`. It retains current
status/lifecycle/events, exact owner/kernel proof, fresh backup and recovery
receipts, all before/after inventories, readiness and mailbox-byte preservation
checks, recurrence analysis and reproducer artifact hashes. Its `SHA256SUMS.json`
binds the packet. No credentials are included in this repository record.
