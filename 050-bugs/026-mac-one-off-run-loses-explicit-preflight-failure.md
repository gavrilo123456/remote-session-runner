# BUG-026 — Mac one-off `run` loses an explicit preflight failure

## Summary

| Field | Value |
| --- | --- |
| Status | `FIXED — DEV/QAS VERIFIED; PRD ACCEPTANCE NOT RUN` — one-off child-shell correction passed installed Dev and QAS acceptance |
| Severity | High — a bounded preflight failure became `lost`/incomplete and may retain Mac execution capacity |
| Priority | High |
| Reported | 2026-10-24 |
| Discovered by | Factory implementer during isolated P09-T02 argv-test preparation |
| Owner | Remote Session Runner |
| Affected route | External file mailbox → `mac-dev` / `local/mac-workstation` → resolved `mac-workstation-qas` |
| Execution identity | Mac account `tomasz.walczuk`, UID 502 (asserted by the request script) |
| Affected component | Mac-local persistent-shell command capture and ephemeral `run` teardown/reconciliation |
| Affected revision | QAS Runner build at incident time was not recorded. Factory checkout HEAD was `c5da1c612e2d613317e09d28c30e093e04cc6775`; this is not the Runner build identity. |
| Related records | [BUG-011](011-mac-local-executor-lost-command-slots-block-local-execution.md), [BUG-015](015-sandbox-remote-terminal-lost-before-gitea-monitor.md), [BUG-024](024-mac-run-command-succeeds-but-teardown-is-lost.md) |

## Observed behavior

A fresh, bounded Mac `run` request was intended to verify source pins and then
run one isolated argv unit test. Its source-pin table contained a transcription
error in the expected SHA-256 for
`code/tests/test_p09_mac_sandbox_runtime.py`: the expected value omitted the
`a` after `46`. The file on QAS matched the correct pin; this was a request
construction error, not source drift.

The mismatch branch printed a bounded diagnostic and executed `exit 1`. Runner
accepted and started the command, emitted the first three passing pin checks,
then emitted the mismatch diagnostic. About 4.5 ms later it emitted
`command_lost`. The result has no exit code, incomplete output, and lost
teardown. Pytest was never invoked and no JUnit report was produced.

This report separates the two facts: the malformed pin caused the preflight to
fail; the explicit `exit` path caused the result to be represented as lost
rather than an ordinary, inspectable nonzero completion. No Runner source defect
or installed-build identity has yet been established.

## Exact incident identity and result

| Field | Value |
| --- | --- |
| Request | `req-asf-p09-argv-unit-after-recovery-20261023-349` |
| Idempotency key | `asf-p09-argv-unit-after-recovery-20261023-349` |
| Job | `job-c94f2ae5e541766a8e906b49084c863b` |
| Session | `sess-3312ec933b87677f4e0df158c93ca0a1` |
| Command | `cmd-e524fdc8d0e0d3d9309bf8722ad28122` |
| Resolved lane | `mac-workstation-qas` |
| Response observation | `2026-10-22T23:05:12.413053Z` (2026-10-23 01:05:12 CEST) |
| Response revision / event cursor | revision 3 / final and available sequence 7 |
| Request / job / command | `complete` / `lost` / `lost` |
| Exit code | absent |
| Output | `output_complete=false`, `output_truncated=false`, `output_unavailable_reason=capture_boundary_unconfirmed` |
| Teardown | `lost` |
| Test execution | Not started; no JUnit |
| ACK / recovery | No ACK. Exact #349 process teardown and retained-capacity disposition are unresolved in the archived observation. |

`request_state=complete` is the mailbox request lifecycle state; it does not
mean the command succeeded or that teardown completed.

## Reproduction details

The archived request used a strict shell and stopped on the pin mismatch before
entering pytest. The relevant control flow was:

```bash
set -euo pipefail
# ... compare each expected pin with the QAS file ...
if [[ "$actual" != "$expected" ]]; then
  printf 'source_pin_mismatch ...\n' >&2
  exit 1
fi
# ... isolated pytest would run only after all pins pass ...
```

The incorrectly transcribed expected pin was:

```text
1a04bda22e5f8b9b91db8061d60398459eba770af46fe9abe21436b20a9c7a9
```

The actual file and approved continuation pin were:

```text
1a04bda22e5f8b9b91db8061d60398459eba770af46afe9abe21436b20a9c7a9
```

The only difference is the missing `a` immediately after `46` in the request.
The mismatch diagnostic was correctly bounded and did not expose secrets.

Event sequence:

| Sequence | UTC time | Event |
| --- | --- | --- |
| 1 | `2026-10-22T23:05:11.440544Z` | `command_queued` |
| 2 | `2026-10-22T23:05:11.449449Z` | `command_started` |
| 3 | `2026-10-22T23:05:11.888143Z` | First source pin passed |
| 4 | `2026-10-22T23:05:12.077305Z` | Second source pin passed |
| 5 | `2026-10-22T23:05:12.281188Z` | Third source pin passed |
| 6 | `2026-10-22T23:05:12.387481Z` | Fourth pin mismatch written to stderr |
| 7 | `2026-10-22T23:05:12.391948Z` | `command_lost` |

Three pins passed. The fourth pin failed only because the request had the
transcription error. The remaining test pin and P00 manifest pin were therefore
not evaluated by this execution.

## Contract overlap and triage question

The Runner documentation at the incident's original contract boundary says
that an explicit shell
`exit` can invalidate the persistent Bash and be reported as `lost`/incomplete,
even for `run` with an ephemeral session. See the [CLI result contract](../docs/user-guide.md#strict-shell-result-semantics),
[API contract](../docs/api.md), and [operations guidance](../docs/operations.md).
The observed classification was therefore consistent with that published
contract. The defect is the one-off operational boundary: a simple script
validation failure in a one-off `run` can destroy the normal command result
and leave teardown/capacity uncertain. The source correction below gives
one-off runs their own script boundary while preserving persistent-session
semantics.

Do not weaken truthful loss handling. If the shell/control boundary or child
termination cannot be proved, retain `lost`/incomplete and the applicable
reservation. Never synthesize a complete result or release capacity from the
script's printed message alone.

## Evidence archive

The complete request, response, seven-event prefix, reconstructed output, and
recovery note are preserved in the factory workspace:

[`#349 evidence directory`](/Users/tomasz.walczuk/projects/agentic-software-factory/implementation/evidence/P09/runner/req-asf-p09-argv-unit-after-recovery-20261023-349/README.md)

| Artifact | SHA-256 |
| --- | --- |
| `request.json` | `9f7fc6fa8e5ed080bee23751fa6ed781c4a5ae92b94ff8f3282274feb4c375d9` |
| `response.json` | `6ab79eb5221b4ac0f9b371967c7ca40615435c9b0e4cda67bc4dc64cd50aac97` |
| `events.ndjson` | `2d9212eb8c9de3ac06d15e017441c40fc0f48ac21a57a3bbeff7a21696d2e22a` |
| `stdout-reconstructed.txt` | `815b2876d4e3b56995f36ecdeffc67052dd0184f34a5398c748c70ad0620896e` |
| `stderr-reconstructed.txt` | `0243b343425883be2dece5c4087eb677d4d187fe1e91674308903cc6df91de4f` |
| `README.md` recovery binding | `b34cd46a728653536de72864c2d75401598426e71f3575f4d8a0a31d839dc8fc` |

The prior #348 cleanup receipt applies only to #348. It does not prove #349
stopped or release #349 capacity. Preserve #349's request and result; do not
replay or ACK it as a substitute for exact cleanup evidence.

## Investigation requested

1. Establish the QAS Runner build and configuration that handled this request,
   if retained deployment evidence allows; otherwise keep it `unknown`.
2. Trace the exact Mac-local `run` path from explicit shell exit through
   control-frame capture, stdout/stderr draining, child/process-group wait,
   ephemeral close, terminal projection, and reservation release/retention.
3. Preserve the completed exact #349 recovery and ACK disposition below; do
   not repeat recovery or infer it from #348's separate maintenance.
4. On an isolated disposable fixture, compare a normal strict-shell failure
   (`false` under `set -e`) with explicit `exit 1`, separately for ephemeral
   `run` and persistent `exec`. Record whether lost output is genuinely
   unprovable and whether cleanup evidence is retained.
5. Determine whether one-off `run` can provide a safe, supported way to return
   the script's nonzero status and complete captured output after an explicit
   exit, while still retaining `lost` whenever streams or descendants remain
   uncertain.

Use no credentials or provider login. Do not run the original #349 request
again. Any fresh QAS test needs a new request/idempotency identity after the
maintainer confirms the prior runtime cleanup and capacity disposition.

## Acceptance criteria for a correction

- A malformed pin or other ordinary preflight failure is reported with an
  accurate terminal result; it cannot be mistaken for test execution.
- Where the shell exit, output boundary, descendant state, and teardown are
  independently proven, the one-off result exposes its actual nonzero exit
  status, complete bounded output, and closed teardown, then releases only its
  owned capacity.
- Where any of those proofs fail, the result remains truthfully lost/incomplete
  and the matching reservation is retained until supported recovery proves
  cleanup.
- Regressions cover normal strict-shell failure versus explicit exit for
  one-off `run`, persistent `exec`, output-drain completion, detached child
  retention, and no replay or unrelated-capacity release.
- A fresh harmless installed Mac/QAS request validates the chosen contract and
  completes the required ACK and zero-retained-capacity postflight. Do not use
  source-only tests or a different lane as installed QAS acceptance.

## Status history

- 2026-10-24 — Registered from the preserved #349 failure. No correction,
  installed-build identification, cleanup, ACK, or QAS acceptance is claimed.
- 2026-10-24 — Source correction committed as
  `ba0eb1b658acdbe27a5049ef492efe1844550883`; full Mac/Factory/Sandbox source
  gates, Mac race checks and Linux-host focused/native gates pass. Installed
  QAS acceptance remains pending. Newer
  handoff evidence confirms #349's already-completed recovery and ACK;
  its original lost result is preserved.
- 2026-10-25 — The Dev-accepted release `rr-20261024-43a4e675` passed the
  installed QAS explicit-`exit 1` Mac-local probe: failed/exit 1, complete
  diagnostic output, event cursor 4, and closed teardown. A separate dedicated
  QAS filesystem-mailbox request completed and its exact response was ACKed;
  lifecycle status confirmed `terminal_acknowledged`. See the
  [PROMO-08 rollout evidence](../040-implementation-evidence/PROMO-08-first-dev-qas-rollout.md).

## Current #349 disposition

The later [handoff closeout](/private/tmp/rsr-qas-prd-return-review-20261023/final-postflight-20261024/handoff-closeout.json)
records the exact #349 pair above as recovered, with its command slot and
session reservation released at `2026-10-23T21:30:39.660215Z`. It also records
all 81 prior terminal responses acknowledged, including #349. Its original
lost result, absent exit code, incomplete output and seven-event history remain
preserved, without replay. The unresolved statements above describe the
original archived observation; they are not a current cleanup instruction.

## Root cause and source correction

BUG-015 corrected ordinary `errexit` failure in a sourced persistent Bash,
and deliberately retained explicit `exit`/`exec` as lost boundaries. One-off
jobs reused that same parent-shell sourcing path even though they do not need
state to survive for another command. An explicit `exit` therefore prevented
the parent from writing its completion frame. The original installed build
remains unknown; current source and disposable fixtures establish this shared
mechanism, rather than proving a regression in a particular installed build.

The correction identifies a one-off command from its authoritative stored
job/command/session binding. Mac and Linux adapters then use the shared
one-off path, which sources the unchanged materialized script in a Bash
subshell within the existing owned process group. Reserved control descriptors
are closed in the child. Its exit or `exec` status returns to the parent,
which emits the normal completion frame and waits for both output FIFOs.
Normal session cleanup must still prove teardown before releasing its
reservation. There is no script rewriting, schema change, retry or replay.

Persistent-session commands keep their existing stateful sourcing and lost
semantics. Actual parent death, unconfirmed output, output-persistence failure
and unconfirmed cleanup keep their existing conservative outcomes. The
candidate does not change historical results or automatically recover work.

Source verification and delivery are recorded in
[BUG-026 implementation evidence](../040-implementation-evidence/BUG-026.md).
Installed Dev and QAS acceptance passed on `rr-20261024-43a4e675`, including
the fresh explicit-exit result and dedicated QAS mailbox/ACK gate. PRD was not
deployed or tested by this rollout and remains outside its scope.
