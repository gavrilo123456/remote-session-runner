# BUG-031 — Mac cleanup retains zombie-only groups and loses teardown failure details

| Field | Value |
| --- | --- |
| Status | `TRIAGED` — native adapter reproduction passed; preventive correction not implemented/deployed |
| Severity | High — completed work can retain session capacity and block subsequent qualification |
| Reported | 2026-11-11 |
| Components | `runtime/mac.go`, `runtime/ownership.go`, `execution/service.go`, `queueworker/worker.go` |
| Affected service source | `126320552acf0a2609d7d75c09016b867b1dbd13`, release `rr-20261108-bug029-1263205` |
| Evidence | [R102 recovery and recurrence investigation](../040-implementation-evidence/R102-completed-teardown-recovery-and-recurrence-2026-11-11.md) |

## Problem and confirmed causes

A command can finish with a truthful ordinary failure, exit code and complete
output, while its ephemeral session teardown becomes LOST and retains session
capacity. R100 and R102 both exhibited this boundary. Exact guarded recovery
released their individual Runner reservations without replay or ACK. That
operational recovery does not fix normal teardown.

Two source defects are confirmed:

1. After reaping its Bash, Mac cleanup waits 500ms for `processGroupExists` to
   return false. Darwin's implementation uses `kill(-PGID, 0)` and treats
   success **or EPERM** as presence. A group containing only an unreaped zombie
   can return EPERM, so normal cleanup retains its marker/capacity despite no
   runnable member. Linux already uses a live-member distinction; this report
   concerns the Mac cleanup path.
2. `markClosingLost` records only generic `runtime_cleanup_unconfirmed` and a
   cleanup-failure count. The queue worker returns on a terminal job before its
   generic error log. The detailed cleanup cause and relevant process-group
   evidence are consequently absent from the retained incident telemetry.

The exact historical physical trigger of **R100 and R102 remains UNKNOWN**.
Their approximately 514ms and 523ms close intervals strongly match the 500ms
group check, but no preserved member snapshot establishes zombie-only state,
delayed live descendants, inspection failure or another cleanup error then.
Do not present the reproduced mechanism as proven production attribution.

## Native disposable reproduction

The task-owned real `MacProcessAdapter` Go-overlay test:

1. Starts a private persistent Bash/session and completes an exit-2 command.
2. Leaves one zombie in the original shell group, retained by its fixture
   parent in a different group; reaps the original Bash.
3. Confirms only native Status5 in that original group.
4. Calls actual `CleanupContext`: it fails at 500ms with a residual-group
   error and leaves the owner marker intact.
5. Lets the fixture parent reap its child; cleanup then succeeds.

The overlay test passed using Go1.27.1 (package 1.300s), with no tracked edits,
live service operation, qualification replay or unrelated process signal.
A separate live redirected-child fixture correctly stayed blocking beyond
500ms. Both fixture groups disappeared after their natural exit/reaping.
Native inspector UID0/PPID0 values for the listed defunct PID are placeholders
after ESRCH, not proof of a root-owned process.

Artifacts: `/private/tmp/rsr-r102-cleanup-repro-verified-toqz3ked/README.md` and
`actual-adapter-result.json`; the latter SHA-256 is
`0254c65630c613da430e21a21865e5f0cace58701f0e4d80b67212b4f01c6772`.
The recovery investigation records their hashes and exact incident identities.

## Narrow implementation instructions

Change **normal post-reap Mac cleanup only** to use bounded native exact-group
inspection. Treat verified zombies/gone members as inert; retain refusal for
any runnable member or incomplete, changing, foreign, denied, truncated or
unknown evidence. Preserve session/root generation, UID/start identity,
context cancellation and owned-workspace/marker guards. Do not globally treat
EPERM as absence, weaken other recovery semantics, kill residuals blindly or
claim coverage of arbitrary detached descendants. A longer timeout alone
cannot fix an unreaped zombie.

Retain a stable sanitized cleanup stage/category and bounded process metadata
at refusal, including terminal-job paths. Bind it to session/generation and
root/group identity and observation/deadline. Distinguish root wait, live
residual group, zombie-only state, inspection denied/incomplete, context
cancellation, workspace removal and marker-finalization failure. Preserve
error wrapping for callers. Avoid raw argv, scripts, environment, output,
credentials and unbounded process listings.

Required native regressions: zombie-only cleanup succeeds; transient live
member exits within the bound; live/mixed groups remain blocked; inspection
failure/overflow/membership race and cancellation retain evidence; terminal
jobs emit exactly one sanitized diagnostic. Verify original failed result,
exit/output/event cursor and ACK state are unchanged, and capacity is released
only after cleanup proof. Keep supported exact completed-teardown recovery
tests and pending-finalization boundaries intact.

Qualify changed service bytes through the ordinary Dev → QAS → PRD flow.
Neither R100/R102 recovery nor this record authorizes replacing a sealed
service or bypassing Factory's reservations and eligibility.

## Relationship to BUG-024

[BUG-024](024-mac-run-command-succeeds-but-teardown-is-lost.md) remains the
historically resolved original incident and bounded observation/recovery
change. This new report tracks the reproduced zombie distinction and missing
failure diagnostics in the later installed descendant, without relabelling
old acceptance or asserting that every conservative LOST teardown is a bug.
