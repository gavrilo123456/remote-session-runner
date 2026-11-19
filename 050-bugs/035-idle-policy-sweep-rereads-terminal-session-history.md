# BUG-035 — Idle policy sweep rereads terminal session history

## Summary

| Field | Value |
| --- | --- |
| Status | `FIXED — DEV/QAS/PRD VERIFIED; IDLE CPU OBSERVED` |
| Severity | High |
| Reported | 2026-11-18 |
| Affected component | Shared execution policy sweep and authority session selection |
| Observed runtime | Linux PRD `rr-20261117-bug033-fd20b1e`, source `fd20b1ebb5b551a1e9d143082101ae5d8843dd7b` |
| Investigation source | `d240edfe9e86ab15b40ff3cefd5ba0150ecd028b` |
| Corrective revision | Combined BUG-034/035 source `7b11dbc08ad09eb5783c8ab3e5b33917ad2901d6` |
| Verification | Four-host full native gates passed on exact `7b11dbc`, including Linux's own suite; the same sealed release passed all eight Dev/QAS/PRD gates and fresh PRD postflights. A fresh 30-second sample observed the installed daemon at about 0.10% of one CPU; host steal was 6.83%. No purge or retention change |
| Owner decision | Fix the history scan and retain records; do not force-purge history |

Current rollout evidence: [Dev/QAS rollout and completed PRD closeout](../040-implementation-evidence/BUG-034-035-dev-qas-deployment-2026-11-19.md).

## Evidence and limits

The owner reported high PRD CPU with no active clients. A bounded live process
sample identified PID `1729186` in `runnerd.service`, running the selected
unsuffixed PRD binary. It consumed 2.32 CPU seconds over approximately 5.70 wall
seconds (40.72% of one CPU). Dev and QAS each consumed about 0.18%. Aggregate CPU
steal was 91.36%; a separate source-test sample measured 93.07%. These are
distinct observations, not proof that Runner causes the hypervisor's steal.

Read-only schema-40 diagnostic counts found:

| Lane | Session history | Nonterminal sessions | Held session / command capacity |
| --- | ---: | ---: | --- |
| PRD | 313 closed, 1 lost | 0 | 1 / 1 |
| Dev | 17 closed | 0 | 0 / 0 |
| QAS | 16 closed | 0 | 0 / 0 |

PRD commands were all terminal: 287 succeeded, 26 failed and one lost. The held
session is the existing GPU request's `sess-6c19a72c5b7df1f3714bfad2de2bd09b`.
It is not evidence of a currently executing command. These aggregate diagnostic
queries are not a supported complete recovery inventory or exit proof.

The source confirms an avoidable history-proportional path:
`EnforcePolicies` calls `ListSessions`, which selects every session ID and
decodes every full record. Only afterward does the policy loop skip terminal
sessions. Linux schedules this sweep once per second. A slow pass can keep the
ticker pending and consume CPU continuously. The relevant Linux execution and
store code is unchanged between the observed release and investigation source.
This proves the algorithmic defect; CPU profiling has not established its exact
share of the observed live CPU usage. Inbound background status reads remain a
possible additional contributor.

After the separate failed source-test group exited, a **20:34:53 UTC** sample
still found the same PRD process at **32.94% of one CPU**, versus about **0.20%**
for Dev and QAS. Aggregate CPU steal was **92.73%**. This shows that the earlier
source test is not required for the observed PRD activity; it does not attribute
that CPU to a particular function. The host reports `perf_event_paranoid=4`,
and no live profiler was attached.

A consistent diagnostic copy taken at **20:23:25 UTC** is **6,664,192 bytes**,
SHA-256 `697f1cc2388d4c7b5117805229aac76a6214338dec9e638b0015acfe37124f05`.
It reports schema 40, successful quick check and zero foreign-key violations.
The oldest session/command/job update is about **19.09 days** old; none is older
than 90 days. These are age counts, not a reference-safe deletion manifest.
No live record was deleted, and the owner chose to retain history and fix its
repeated scanning cost.

Private observations: `/private/tmp/rsr-linux-prd-idle-cpu-readonly-rf30sdm0/`.
The initial readiness GETs updated only health-probe timestamps; that limitation
is recorded in `READ-ONLY-QUALIFICATION.json`. Subsequent database counts used
read-only connections. At that initial diagnostic checkpoint, no project work, capacity, service,
selector or configuration was changed; later owner-approved stops are recorded below.

## Source correction

1. Add a dedicated indexed selection of nonterminal sessions before full-row
   decoding. Preserve deterministic ordering and validation of selected rows.
2. Use it only for normal policy enforcement. Keep complete history, startup,
   diagnostics and recovery inventories available through their existing paths.
3. Preserve timeout, idle and lifetime enforcement, parent cancellation,
   serialized mutations, retained LOST capacity and all terminal results.
4. Prove that large terminal histories are excluded from policy decoding and
   that active sessions still receive their required enforcement. Prefer
   structural tests over narrow timing assertions.
5. Qualify the exact corrective source through the repository Git/native-test
   workflow. Source verification does not deploy a lane or establish a live
   before/after CPU reduction.

The source now adds `ListNonterminalSessions`, selecting the five nonterminal
states through the existing session-state index before full-row validation,
in deterministic creation/ID order. Only `EnforcePolicies` uses this selection.
The complete `ListSessions` path, startup/recovery and retention are unchanged;
the policy loop retains its final terminal-state guard for a state change
between selection and reread.

Mac focused tests and vet passed with Go 1.27.1, UID/EUID 502, GID/EGID 20,
normal shared caches and child umask `022`. Tests prove 512 terminal rows are
excluded before decoding, all five active states and index use, cancellation
and validation errors, active idle/lifetime/command limits, and preservation of
a real LOST result, events, reservation and command slot. The first new
fixture failed because it requested an idle limit above the existing ceiling;
that result is preserved, and only the fixture was corrected to the allowed
limit. No production limit was changed.

A separate private-copy comparison retains all 314 historical sessions. Its
full inventory returns 314; the corrected policy query returns zero candidates.
Mac query timing corroborates the reduced work, but does not establish live
PRD CPU reduction or installed acceptance. Exact combined-source full qualification passed on all four hosts, as recorded
in the evidence. It does not establish installed performance. Separate owner-approved
Linux/Mac stops and their limits are recorded in that current checkpoint.

See [BUG-035 evidence](../040-implementation-evidence/BUG-035.md) for exact
commands, timings, source boundaries and private evidence paths.

## Boundaries

Retain all production history and both original BUG-034 GPU exchanges in their
unACKed state. The original LOST hold was preserved during source work; its
later exact, separately approved cleanup released only that reservation/slot.
Do not replay, ACK, release other capacity, lower retention or invoke internal
GC as a shortcut.

No public force-purge/preview command currently exists. Internal authority GC
has 30-day output and 90-day metadata defaults and cannot independently see
Mac-side unACKed references. A diagnostic database copy is not an operational
catalog backup or permission to delete live records. General retention-policy
engineering remains separate from this targeted policy-sweep correction.

## Installed checkpoint — 2026-11-19

The history-preserving correction is now deployed through Dev → QAS → PRD in
`rr-20261119-bug034035-7b11dbc`. PRD's complete operation, all eight gates and
fresh selected-release/schema/readiness/inventory checks passed; the
[common closeout](../040-implementation-evidence/BUG-034-035-dev-qas-deployment-2026-11-19.md#prd-closeout--2026-11-19)
records the exact proof. No historical records were purged. From
**08:04:34.242387–08:05:04.250639 UTC**, the actual sealed `runnerd` daemon
PID217781/start3497039 used 3 ticks at HZ100 over 30.00824164 seconds:
**0.03 CPU seconds, about 0.10% of one CPU**. Its sealed log-capture supervisor
(systemd MainPID217770) used zero ticks. Both exact component hashes,
incarnations, UID1001, parent/cgroup, selector and boot stayed stable; the unit
was active with zero restarts. Host steal was separately **6.83%** on two CPUs.
The initial private sampler refused its incorrect MainPID-as-daemon assumption;
only that observer was corrected. No production repair, DB query or signal was
needed. This is a bounded installed observation, not a controlled before/after
benchmark or an explanation of changed steal. BUG-036 remains TRIAGED/unfixed.
