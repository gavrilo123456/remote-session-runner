# BUG-012 — Scheduler can misorder queued commands stored with variable-width RFC3339 timestamp text

## Summary

| Field | Value |
| --- | --- |
| Status | `CLOSED` — source correction and required source-only verification passed; it is not installed on a service |
| Severity | High |
| Priority | High — the fair scheduler contract chooses the oldest eligible durable command |
| Reported | 2026-10-03 |
| Discovered by | Required B011-P4 primary Ubuntu source regression |
| Affected component/path | `src/internal/store/scheduler.go`; the B008 regression helper used the same incorrect text ordering |
| Affected revision | Confirmed at `68b962f45fac6e7f9bbb1c06f9ba01903c39fbcd`; its P4 diff did not modify `runnerd`, `queueworker`, or `store` |
| Fixed revision | `70283c97d0b54f34dd743f3f5798095bdb16851f` (`fix(BUG-012): preserve chronological queue ordering`) |
| Verification | Focused store/B008 regression, full Mac source gates, clean matching primary and sandbox Ubuntu source regressions; no installed service restart. |

## Reported behavior

The required primary Ubuntu source regression reported the later retained-capacity
command before the earlier one when its test helper read `command_started` rows
with SQLite `ORDER BY occurred_at, command_id`. Separate source inspection found
the same lexical timestamp-text ordering in the authoritative queued-command
scheduler.

## Expected behavior

The scheduler must choose the chronological oldest eligible command, then use
the existing session and ordinal tie-breakers. This is the fair host scheduler
contract in the detailed design and P019; timestamp spelling must not alter it.

## Reproduction evidence

```text
Machine and account: primary Ubuntu — ubuntu@oracle-yuta-konopka-ubuntu-micro-02
Source revision: 68b962f45fac6e7f9bbb1c06f9ba01903c39fbcd
Command: GOTOOLCHAIN=local "/home/ubuntu/.local/share/remote-session-runner/toolchains/go1.27.1/bin/go" test ./src/internal/queueworker ./src/internal/runnerd -run '^(TestBUG007.*|TestBUG008.*|TestBUG009.*|TestRecoverLostRuntime.*|TestNewRejectsIncompleteConfiguration|TestWorkerStartWakeStopWithoutQueuedWork)$' -count=1
Observed result: TestBUG008DispatcherStartsPreservedQueuedOneOffsAfterRetainedCapacityRelease reported durable command start order=[cmd-bug008-dispatcher-release-second cmd-bug008-dispatcher-release-first], want [cmd-bug008-dispatcher-release-first cmd-bug008-dispatcher-release-second]
```

The persisted format was `time.RFC3339Nano`, which may omit trailing fractional
zeroes. For example, chronological `.1Z` precedes `.100000001Z`, while a
lexical SQLite `TEXT` ascending sort places `.100000001Z` first.

## Impact and scope

`StartNextEligibleCommand` previously ordered `exec_commands.created_at` as
SQLite text, so the failure can affect a real oldest-command claim. The B008
helper separately could report a non-chronological event order by ordering
event timestamp text the same way. The failed helper read does not by itself
prove that the production scheduler made a reversed claim; source inspection
of `StartNextEligibleCommand` establishes the scheduler risk.

This correction is deliberately narrow: the scheduler parses and sorts every
eligible candidate in Go, so it is safe for existing variable-width fractional
second rows and any RFC3339Nano spelling accepted by `parseStoredTime`. It does
not migrate any installed database or alter an installed service. Other
timestamp-based SQLite ordering sites are outside this correction and are not
claimed fixed by this record.

## Investigation

1. The P4 diff contains no changes in `runnerd`, `queueworker`, or `store`.
2. `formatStoredTime` writes `time.RFC3339Nano`; `parseStoredTime` already
   safely accepts those values.
3. `StartNextEligibleCommand` selected queued work with `ORDER BY c.created_at`.
4. The shared queue worker serially makes durable claims before launching
   execution, so concurrent execution does not explain a reversed claim order.

## Corrective rerun pre-phase record

| Item | Value |
| --- | --- |
| Authoritative source checkout | Mac `/Users/tomasz.walczuk/projects/remote-session-runner` as `tomasz.walczuk` |
| Source base | `68b962f45fac6e7f9bbb1c06f9ba01903c39fbcd` (`phase(B011-P4): wire shared local queue worker`) |
| Fresh-context read | Root `AGENTS.md`; initial design; full detailed design; full detailed phased plan; `040-implementation-evidence/000-preimplementation-decisions.md`; BUG-008, BUG-009, BUG-011, and this record; prior B011 evidence; current scheduler, worker, and B008/P019 tests |
| Intended correction | Keep B011-P4 halted while repairing the shared store fairness selection and its regression helper; no locald/runnerd service, schema, mailbox protocol, runtime adapter, or live record change |
| Required rerun | Focused store and B008 tests; B011-P4 Mac source/race/full gates; explicit-key push; clean fast-forward and source-only regression on both Ubuntu hosts at the same SHA |
| Service boundary | No installed service will be restarted, installed, stopped, reconfigured, or used for this corrective source work. |

## Fix plan

1. Read `created_at` and ordinal for every eligible durable candidate.
2. Parse `created_at`; fail safely with `ErrCommandOrderCorrupt` if it is not a
   valid stored timestamp.
3. Sort parsed candidates chronologically, followed by existing session,
   ordinal, and command-ID deterministic ties, before the current claim
   transaction proceeds.
4. Reject malformed candidate timestamps before a state transition or slot
   reservation.
5. Make the B008 verification helper parse its event timestamps before it
   verifies the two durable starts.
6. Add a store regression using `.1Z` followed by `.100000001Z`; prove the
   earlier command is selected even though lexical SQLite order is reversed,
   and a corruption regression that retains the queued boundary.
7. Repeat the halted B011-P4 gates and both clean Ubuntu source regressions.

## Correction and verification

The correction reads every eligible candidate's `created_at`, parses it with
the existing `parseStoredTime`, and orders candidates chronologically before it
makes the current durable claim. An invalid candidate timestamp returns
`ErrCommandOrderCorrupt` before command state or capacity changes. The B008
helper now parses event timestamps before it verifies start order.

Mac source verification passed at the fixed source revision:

- focused scheduler and corruption tests;
- B008 retained-capacity test repeated ten times;
- B011-P4 locald, local API, shared-worker, compile-only, race, full test,
  vet/build/smoke, import-boundary, formatting, and diff gates.

The Mac pushed `70283c97d0b54f34dd743f3f5798095bdb16851f` to GitHub `dev`
with the configured explicit key. Both clean Ubuntu `dev` checkouts
fast-forwarded with their configured explicit GitHub keys and matched that
exact SHA:

| Host | Source-only result |
| --- | --- |
| `ubuntu@oracle-yuta-konopka-ubuntu-micro-02` | shared-worker regression **PASS**; `TestP019D17Scheduler.*` **PASS** |
| `ubuntu@oracle-gustaw-janecki-ubuntu-flex-02` | shared-worker regression **PASS**; `TestP019D17Scheduler.*` **PASS** |

These runs used only the project checkouts and selected Go toolchains. They
did not restart, install, stop, or reconfigure a service; they did not touch a
mailbox, live database, public endpoint, or live command.

## History

| Date | Change |
| --- | --- |
| 2026-10-03 | Recorded after the primary Ubuntu B011-P4 source regression failed. No installed service was restarted, installed, or reconfigured. |
| 2026-10-03 | Fixed in `70283c97d0b54f34dd743f3f5798095bdb16851f`; Mac and both clean Ubuntu source regressions passed. No installed service was restarted, installed, or reconfigured. |
