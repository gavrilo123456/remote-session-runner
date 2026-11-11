# BUG-031 — Mac cleanup retains zombie-only groups and loses teardown failure details

| Field | Value |
| --- | --- |
| Status | `FIXED — DEV VERIFIED; QAS/PRD PENDING` — four-host Dev cohort acceptance passed; Mac installed failed-exit2 cleanup regression passed |
| Severity | High — completed work can retain session capacity and block subsequent qualification |
| Reported | 2026-11-11 |
| Components | `runtime/mac.go`, `runtime/ownership.go`, `execution/service.go`, `queueworker/worker.go` |
| Affected service source | `126320552acf0a2609d7d75c09016b867b1dbd13`, release `rr-20261108-bug029-1263205` |
| Evidence | [Dev deployment closeout](../040-implementation-evidence/BUG-031-dev-deployment-2026-11-11.md); [BUG-031 implementation and verification](../040-implementation-evidence/BUG-031.md); [R102 recovery and recurrence investigation](../040-implementation-evidence/R102-completed-teardown-recovery-and-recurrence-2026-11-11.md) |

## Dev deployment — 2026-11-11

Release `rr-20261111-bug031-0b7c031` is installed and ready on all four Dev
hosts. Operation `aaef98e9a1974c1ba0de0b5f8ea8dde7` completed with all eight
acceptance gates passed and identical released cohort receipts. The ordinary
Mac Dev exit-2 regression preserved the failed result and complete output,
returned cursor4, closed teardown and released capacity; error counters stayed
zero. Final inventories are empty, schema40 and configuration identities
unchanged. [Exact deployment and postflight evidence](../040-implementation-evidence/BUG-031-dev-deployment-2026-11-11.md).

QAS and PRD retain their prior release and mailbox assignments. Filesystem-
mailbox acceptance and live injected-zombie acceptance are NOT RUN. Native
disposable fixtures supply the zombie-specific proof. No historical
qualification, ACK, R100/R102 recovery or Factory hold was changed.

## Source correction — 2026-11-11

Commit `0b7c031d7025b79e712bc52e73367a41bdefcf4e` corrects normal post-reap
Mac cleanup and retains sanitized failure diagnostics through terminal jobs.
The source is delivered through primary Gitea and its exact GitHub mirror;
all three Ubuntu checkouts have fast-forwarded to it.

Native inspection now positively distinguishes zombies from live processes.
It verifies UID, group and process start identity across bounded repeated
snapshots. A verified zombie-only or empty group can finish cleanup; runnable,
mixed, foreign, changing, denied or incomplete evidence still refuses it. The
500ms grace is unchanged. Neither EPERM nor ESRCH alone proves absence, and
existing LOST-recovery inspection and signalling rules are unchanged.

Cleanup causes remain wrapped through execution. The terminal-job path emits
one warning with validated stage/category, UTC observation/deadline, exact
Runner identity bindings and bounded process metadata. It excludes raw errors,
scripts, arguments, paths, environment and command output. Invalid diagnostic
metadata produces an explicit unavailable category. Metadata is evidence,
never authority to signal processes or release capacity.

Mac native regressions and targeted race tests passed. The latest full
test/vet/build/smoke sequence passed on each of the four hosts. Factory's first
full run failed in a separate release-authority concurrency fixture; its
diagnostic and passing single full retry are preserved in the evidence, with
the original failure still NOT PASS and its cause unestablished. At that
source-only checkpoint no sealed release or installed service was changed;
installed-lane acceptance had not run. The later Dev deployment is recorded
above; QAS/PRD remain pending. R100/R102 recovery was not repeated, and their original physical
trigger remains UNKNOWN.

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
