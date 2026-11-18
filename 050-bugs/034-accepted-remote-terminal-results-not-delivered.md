# BUG-034 — Accepted remote terminal results remain undelivered

## Summary

| Field | Value |
| --- | --- |
| Status | `FIXED — PENDING VERIFICATION` — source correction; installed-lane verification pending |
| Severity | High |
| Reported | 2026-11-18 |
| Affected component | Mac remote-run observation, strict terminal reconciliation and mailbox delivery |
| Incident runtime | PRD `rr-20261117-bug033-fd20b1e`, source `fd20b1ebb5b551a1e9d143082101ae5d8843dd7b` |
| Investigation checkout | `ce4761db8bd5cb5819f834559d2fdba8c7437a63` |
| Fixed revision | Source correction `cb7ccf34222ce24b951829c881d4550bc8c61b80`; fixture checkpoint `d240edfe9e86ab15b40ff3cefd5ba0150ecd028b`; combined BUG-034/035 source commit pending |
| Verification | Reproduction failed before the fix. Linux `de19498` ended NOT PASS after three fixture failures and a scoped source-test SIGTERM. Historical `d240edf` passed Mac/Sandbox/Factory full gates; Linux was never launched at that revision. Combined-source Mac full qualification passed; fresh Ubuntu qualification pending. No release built or lane accepted |

## Reported behavior

Two `gpu-services` mailbox requests remain `accepted`, revision 2, without
advertised event cursors, although the Linux authority preserves terminal
records and their original event streams. The environment is `sandbox-dev`;
the persisted execution target is `remote/linux-host`, the replacement Linux
worker `79.72.94.59`, hostname `oracle-waclaw-dziwulski-ubuntu-micro-02`.

| Request | Job | Session | Command |
| --- | --- | --- | --- |
| `req-gpu-f04-wait-tests-20261118-b1` | `job-2b0f802ae026b29d076aa7553433e684` | `sess-6c19a72c5b7df1f3714bfad2de2bd09b` | `cmd-9f5a62eaa2776756368fc2046da05370` |
| `req-gpu-runner-checkout-observe-20261118-h3` | `job-a0a080df73aa028b0ee553c9739459b6` | `sess-a774519e5e66a282a105649ec6c081dc` | `cmd-018085c93bb6b156bb934703b84a6640` |

The Mac router logs repeated `remote_status_unavailable` for those exact
identities. A first pinned SSH read timed out; a subsequent native-context,
pinned bridge GET-only observation recovered both authoritative snapshots and
contiguous event streams. The final mailbox read at 15:15 UTC still showed
revision 2 and no event files or ACK artifacts. This establishes a delivery
failure, not its precise underlying cause.


A later **19:39:39 UTC** read-only follow-up found both original PRD outboxes
still `accepted`, revision **2**, without an advertised event cursor. Their
response hashes were unchanged: API test `6a062d30…474e`, checkout observation
`1a312425…c013`. The earlier incident and 17:33:10 UTC checkpoints remain
preserved in the evidence. This confirms that terminal delivery is still
unresolved; it does not establish fresh teardown or capacity clearance. No
request, ACK, replay, service or mailbox change accompanied the read.
Private proof: `/private/tmp/rsr-bug034-original-outboxes-readonly-6tn5nbum/states.json`.

## Original execution evidence

1. API tests started at 14:44:20 UTC. Original output proves the GPU checkout
   fast-forwarded to `4ea84758f5c86c21dec1150acf286fb294432af1`, with two test
   groups (8 and 3 tests) reporting `OK`. The terminal worker outcome is **LOST**,
   no exit code, cursor **24**, incomplete/untruncated output,
   `capture_boundary_unconfirmed`; teardown is **lost**,
   `runtime_cleanup_unconfirmed`. Overall test success and runtime exit are
   not established.
2. The read-only checkout check started at 14:54:41 UTC and **succeeded**, exit
   **0**, cursor **7**, complete/untruncated output, **closed** teardown. Its
   original output reports a clean GPU checkout on `dev` at the same exact SHA.

The supported Mac inventory completed. A subsequent supported Linux inventory
exceeded its bounded observation; exact current worker capacity disposition
therefore remains unverified. Nothing was released. No replay, replacement,
ACK, service change, configuration change or recovery was performed.

Private incident evidence: `/private/tmp/rsr-gpu-two-nonterminal-20261118-bs73gz4z/README.md`.
Raw command output and host configuration remain outside Git.

## Expected behavior and scope

Bounded GET-only observation must recover truthful terminal results and their
verified event cursors for already-accepted remote runs. A valid LOST result
must remain LOST; publishing it must not imply confirmed cleanup or authorize
capacity release. The successful command must retain its original exit/output
and closed teardown. Transient transport failures must preserve identities and
eventual progress without mutation replay or synthetic success.

BUG-033 concerned initial **uncertain** dispatches. These two intents are
already **accepted** and need separate diagnosis.

## Confirmed source defect

The accepted-result verifier performs four sequential SSH exchanges for an
ordinary terminal run: job, command, events and a fresh job read. The event-gap
path can perform six exchanges, including independent terminal confirmation and
a normalized command refresh. Every exchange creates its own SSH process.
Both the per-intent proof and the rotating batch formerly clamped all these
reads to the same shrinking 15-second window.

A disposable regression reproduced indefinite acceptance with valid success
and LOST responses: each read individually completed within its limit, but
their combined time exceeded the batch window. Eight successive passes per
case still produced no terminal proof and sent no mutation. The baseline
regression failed before production edits. This proves a source starvation
defect consistent with the incident. Historical generic production logs do not
prove the exact failed read or exclude additional transport failures.

The correction retains each read's 15-second bound. Batch time controls when
another intent may start; a started proof gets a separate finite allowance for
its maximum read sequence (six for runs, four for submits). Final snapshot,
script/context, event-boundary and cancellation checks remain required. With
defaults, the maximum I/O allowance is 90 seconds for a run proof and 60 for a
submit proof; a batch may overrun its 15-second admission window by one such
attempt, plus local bookkeeping. One stalled read still stops that attempt
after its own read bound. Initial uncertain-dispatch observation is unchanged.
Allowlisted stage/category diagnostics distinguish future failures without
printing nested errors or credential-bearing transport text.

The aggregate is elapsed time: between-read SQLite work and host scheduling
consume it too. Six/four read allowances are finite ceilings, not a guarantee
that individually timely reads complete under arbitrary host delay. Timeouts
remain possible, and this source reproduction does not establish an exclusive
cause of the historical live failure.

## Correction and verification plan

1. Reproduce the failure on disposable fixtures, checking immutable identity,
   script/context bindings, observation budgets, event mirroring, terminal
   proof and mailbox publication. Record the confirmed cause before selecting
   a correction. This source reproduction is complete; the precise historical
   production stage remains unproven.

Run the disposable regression on the **Mac**, in the canonical checkout, with
the repository's shared cache settings:

```sh
GOCACHE=/private/tmp/remote-session-runner-gocache \
GOMODCACHE=/private/tmp/remote-session-runner-gomodcache GOTOOLCHAIN=local \
"/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/toolchains/go1.27.1/bin/go" \
  test ./src/internal/dispatcher ./src/internal/runnerlocal -run '^TestBUG034' -count=1
```

The pre-correction failure was captured with the new dispatcher regression
applied to the original production code; the corrected source must pass.
2. Implement the smallest correction without weakening identity, event,
   teardown, no-replay, ACK or capacity guards. Cover success and truthful LOST
   results, delayed reads/retries, restart and adverse identity/event cases.
3. Run the relevant Mac tests/vet/build, inspect and commit the intended files,
   push Gitea, verify the exact GitHub mirror SHA, then clean fast-forward and
   run required native validation on the three Ubuntu hosts.
4. Any Dev activation uses the promotion toolkit and its fresh usage,
   backup/capacity and acceptance gates. QAS and PRD deployment are not part
   of this source correction. The original PRD exchanges and capacity remain
   unchanged until separately supported and authorized operations apply.

## History

| Date | Change |
| --- | --- |
| 2026-11-18 | Registered before source correction; original results and unresolved PRD delivery/cleanup boundaries recorded. |
| 2026-11-18 | Reproduced shared-budget starvation, implemented separate finite proof/read budgets and safe diagnostics; corrected dispatcher and disposable mailbox regressions pass. |
| 2026-11-18 | Preserved Linux's 20-minute interruption and later timing-sensitive assertion failures, then stopped only the exact failed source-test group. Test-only correction `de19498` passed Mac full gates; repeated Ubuntu qualification pending. Production guards and original PRD exchanges remain unchanged. |
| 2026-11-18 | Further timing-fixture correction `d240edf` preserves runtime guards; deterministic gap-budget/read-count checks reject five/three-read negative overlays. Mac/Sandbox/Factory full native gates passed; Linux remains gated while the prior failed full suite finishes. No release or lane deployment. |
| 2026-11-18 19:52 UTC | Prior failed `de19498` full run recorded additional unchanged-package passes, including `localapi` after 938.509 seconds. CPU steal was measured at 93.07%; the final result is still pending and Linux `d240edf` remains gated. No cancellation applied or new source qualification inferred. |
| 2026-11-18 20:25 UTC | Preserved all three failed assertions and source logs, then sent SIGTERM only to the exact failed Go test group. Its supervisor recorded test exit `-15` after 5,986.183 seconds and no live group members. Later gates did not run. No Runner runtime was signalled. |
| 2026-11-18 20:29 UTC | BUG-035's history-preserving policy selection and the BUG-009 test-helper final-read correction join the next source candidate. Linux `d240edf` was never launched; its three valid historical host receipts remain preserved. Combined-source Mac full gates subsequently passed; final commit and Ubuntu qualification remain pending. |

Qualification and Git delivery are recorded in [BUG-034 evidence](../040-implementation-evidence/BUG-034.md).
