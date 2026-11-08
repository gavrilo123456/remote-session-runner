# BUG-029 — An unbounded SSH observer blocks mailbox processing while health is ready

## Summary

| Field | Value |
| --- | --- |
| Status | `FIXED — DEV VERIFIED; QAS/PRD PENDING` — installed and accepted on all four Dev hosts |
| Severity | High |
| Priority | P1 — prevents result delivery and independent mailbox intake |
| Reported | 2026-11-07 |
| Owner | Remote Session Runner maintainer |
| Affected component | Mac `runner-local` cycle, accepted remote reconciliation, and SSH reply/EOF handling |
| Affected revision | Installed Mac ingress `e3d3f5a133e58c97040920162a221504610b9e1b`; inspected checkout `6211a93112ba0ac65463ffdb9a89472b9b175322` has identical relevant source paths |
| Fixed revision | `14aadaa4e4c9d6e126f6fab1a71561245a2b0be4` — source correction; Dev release `rr-20261108-bug029-1263205` contains the corrected source and fixture revision |
| Regression fixture correction | `126320552acf0a2609d7d75c09016b867b1dbd13` — test-only budget and diagnostic correction; production code unchanged |
| Verification | Four-host full source gates and Mac affected race/latency controls passed. Dev operation `9dd615f3f0ee4e54b02a8dc1863a3c3d` completed with all eight functional gates, released admissions and passing postflight. Earlier failed attempts remain recorded. QAS/PRD deployment and filesystem-mailbox acceptance are NOT RUN. See [Dev deployment evidence](../040-implementation-evidence/BUG-029-dev-deployment-2026-11-08.md). |

## Reported behavior

A completed Sandbox command remained projected as accepted/running with no
advertised events. A later valid request and empty ready marker remained in
the same inbox without a durable exchange, outbox response, or diagnostic.
The Mac API and remote-route health nevertheless reported ready.

The owning ingress is the unsuffixed Mac PRD service. `sandbox-dev` is the
requested environment policy; its resolved remote target is `sandbox-host`.
It does not identify the Dev service lane.

## Exact incident identities

| Field | Existing policy-details request |
| --- | --- |
| Request | `req-codex-logger-f0da-recorder-policy-details-20261107-02` |
| Job | `job-72eec02666fbc554d65579d199e5ff74` |
| Session | `sess-358bd807828fe45df80b462b6970b2b9` |
| Command | `cmd-4d62a0c82bda807f322784ad898c1ca8` |
| Before restoration | Accepted response revision 2; `job_phase=awaiting_command`, `command_state=running`, no event cursor |
| Worker result | Completed at `2026-11-07 16:34:40 UTC`, before the stalled observer started |
| After restoration | Complete revision 4, event cursor 4, succeeded, exit 0, complete untruncated output, closed teardown |

| Field | Existing recorder-dispatch request |
| --- | --- |
| Request | `req-codex-logger-f0da-trusted-recorder-dispatch-20261107-01` |
| Before restoration | Original JSON and empty `.ready` remained in the inbox; no durable exchange, outbox or diagnostic |
| Job assigned by ordinary intake | `job-b47668248f618629299a2ccb3fcb25d5` |
| Session assigned by ordinary intake | `sess-5a9071f2bea8e0be23156e1b871c0719` |
| Command assigned by ordinary intake | `cmd-9eb7753f4c1f645271d46bbe6a04ffdf` |
| After restoration | Complete revision 3, event cursor 4, succeeded, exit 0, complete untruncated output, closed teardown |

The dispatch input files were ordinary selected-user-owned `0644` workspace
ingress files. That mode is explicitly supported by the
[mailbox contract](../docs/mailbox.md#active-mailbox-roots); changing their
permissions or republishing the request was not the repair.

## Investigation and operational restoration

All times below are UTC on 2026-11-07.

1. Mac PRD `runner-local` PID `4523` retained SSH child PID `22362`, started at
   `16:34:43`. The accepted policy-details command had already completed on
   Sandbox at `16:34:40`. The later dispatch pair was published around
   `16:41` but was not imported.
2. Process identity, parentage, transport arguments and socket evidence were
   captured. The complete remote-intent inventory contained no `dispatching`
   intent; the latest mutation claim was at `16:34:33.269728`, and no remote
   lifecycle transition occurred after the observer's start. Source ordering
   requires a durable mutation claim before spawning its transport, and does
   not record acceptance until that transport exits. This distinguished the
   retained read-only observer from a command-execution or mutation channel.
3. A separate QAS SSH child, PID `22365`, was identified and preserved. The
   healthy API/route probes were not treated as evidence that the mailbox
   processing loop was advancing.
4. At `17:23:50`, one guarded `SIGTERM` interrupted only SSH observer PID
   `22362`. Its exit was observed. The PRD parent identity and QAS child
   identity remained unchanged. No Runner, executor, worker service, process
   group, database row, mailbox input or ACK was manually changed.
5. By `17:24:17`, the existing processing loop had delivered both original
   terminal responses. The policy-details result was recovered from existing
   worker evidence. The original dispatch pair was consumed by ordinary
   intake and executed once; no replacement request or replay was published.
   Both intents retained attempt count 1, and neither request was ACKed.

The original dispatch returned `recorder_dispatch_http=204`. Independent
read-only Gitea observation correlated it to UI run **5673**, API run ID
**5737**, completed successfully:
[Gitea recorder run 5673](https://gitea.devnull.group/admin/slidestud.io/actions/runs/5737).
Its source was `f0da2346981a7cf05042901161d376025c05b4c5`, and its protected
control commit was `66ea316efeec9d23fc277e879399f9be89110efe`. The observer did
not dispatch the workflow separately.

Private incident captures are under
`/private/tmp/rsr-sandbox-reconcile-20261107-7yabtv9o/`, including
`read-only-observer-classification.json`, `observer-cancel-result.json`,
`final-mac-exact-delivery.json`, and `gitea-recorder-final-correlation.json`.
The correlated identities and terminal boundaries above remain part of this
record independently of those temporary captures.

## Root cause and limits

The installed source has an unbounded observation path:

- `src/internal/runnerlocal/serve.go` runs accepted remote reconciliation
  synchronously in the mailbox/dispatch cycle using the service-lifetime
  context (`runCycle`, around lines 1100–1107).
- `src/internal/dispatcher/jobs.go` forwards that context to strict remote
  status reads and terminal event mirroring.
- `src/internal/sshclient/client.go` waits for decoded replies and EOF
  (`exchange`, around lines 303–390). A non-stream call continues reading
  after a valid reply so it can validate EOF and the SSH exit status. There is
  no finite observation deadline at these call sites; SSH `ConnectTimeout`
  only bounds connection establishment.
- Health probing runs in a separate goroutine with its own 12-second
  context. It can continue reporting ready while the main cycle is blocked.

A transport that stays open without completing its read/EOF boundary can
therefore stop later mailbox intake and result delivery indefinitely. Error
aggregation cannot provide fairness until the stalled call actually returns.
The immediate progress after interrupting the exact observer supports this
mechanism. The network or peer condition that originally stranded that SSH
channel is not established by this incident.

This is distinct from [BUG-003](003-runner-local-reconciliation-stalls-mailbox-outbox.md),
which corrected early returns after completed operations reported errors.
[BUG-004](004-mailbox-terminal-result-missing-after-gitea-dispatch.md) has a
related missing-result symptom without this demonstrated cause.
[BUG-016](016-sandbox-mailbox-accepted-without-command-projection-after-restarts.md)
concerns commandless failed preparation and unconfirmed teardown;
[BUG-020](020-post-fix-mac-local-running-without-python-child.md) concerns a
different persistent-shell control-descriptor read. None records this exact
SSH observer mechanism as fixed.

## Source correction and verification checkpoint

The source correction is committed as
`14aadaa4e4c9d6e126f6fab1a71561245a2b0be4` and verified at the same primary
Gitea and GitHub mirror SHA; the installed E3 services remain unchanged.
[BUG-029 evidence](../040-implementation-evidence/BUG-029.md) records
the current results and remaining gates.

Accepted run and submit observations now have a default 15-second aggregate
I/O budget per intent. Run and submit reconciliation each also have an
independent 15-second batch I/O budget. Only allowlisted read-only calls and
event streams derive actual deadline contexts. The aggregate deadline is
carried as metadata so that failure and stale-projection bookkeeping can
still use the live caller context after transport expiry. Parent cancellation
retains precedence over an observer timeout.

Batch cursors advance to the last attempted intent, preserving an unattempted
page tail for the next cycle. Stream event persistence uses its read context.
SSH cancellation closes both parent pipe ends, reaps the direct child, and
joins the cancellation callback; stderr goes directly to the null device so
an inherited descriptor cannot retain an internal copy goroutine in `Wait`.
Reply identity, EOF/exit, event-boundary and teardown proof remain required.

Mutation delivery and execution limits are unchanged. An observer timeout
records unavailable status or a stale projection while preserving the
accepted request and its identities. It cannot create a failed execution,
terminal proof, replay, replacement request, or ACK.

Disposable regressions cover:

1. No reply, a valid reply without EOF, inherited output pipes, blocked stdin,
   and process wait after EOF, including child reaping and cancellation cause.
2. Run and submit batch expiry, cursor progress through the unattempted tail,
   durable status diagnostics, stale projections, and parent cancellation.
3. Later original ready-pair intake while a status/event observation stalls,
   including supported direct-workspace `0644` input.
4. Recovery of the original retained terminal events and teardown after a
   qualified read, with unchanged identities and one mutation per request.

Mac focused and race checks passed. The first full Mac `make test` is retained
as NOT PASS: the capture wrapper applied umask `077`, so an unrelated bootstrap
fixture's intended `0755` unsafe parent became `0700`. The wrapper was
corrected to child umask `022` without changing that fixture or production
bootstrap source. The corrected full Mac test/vet/build/smoke retry passed.
Sandbox and Factory also passed all four initial native source gates. The
owner approved a BUG-029-only HTTPS Git route for Linux. Its first full suite
failed the new batch-tail fixture, which required complete database-backed
reconciliation within an artificial 100 ms budget. A controlled latency
fixture reproduced that timing assumption; the original Linux error hid its
cause and is retained as NOT PASS. Test-only correction `1263205` uses the
shipped 15-second budgets, strengthens transport-order/cursor diagnostics and
retains strict terminal/no-replay checks. Mac full/race and the delayed-read
control passed; corrected full test/vet/build/smoke qualification then passed
independently on all three workers at `1263205`. No production Go code changed
in this fixture correction. At that source-only checkpoint, release construction
and installed-lane acceptance were NOT RUN. The original old-source transport
control also reproduced the inherited output-pipe failure with the new fixture.

Do not reproduce this by dispatching protected project work or by generally
signalling live SSH children. The one-off operational intervention above is
not a new public recovery command. Dev verification does not establish that the
historical PRD incident has received the preventive runtime correction.

## Dev deployment — 2026-11-08

The owner confirmed Dev was unused. The toolkit sealed the four native bundles
as `rr-20261108-bug029-1263205`, then completed the same guarded Dev operation
across Mac, Linux, Sandbox and Factory. All eight functional gates passed;
post-restart readiness, empty active-work inventories, schema 40, open admissions
and unchanged peer-lane identities were verified. The candidate was not rebuilt
after sealing. A separately tested operator correction for
[BUG-030](030-selector-discovery-requires-unrelated-historical-artifacts.md)
allowed preserved historical journals to remain intact during this rollout.

QAS and PRD were not promoted or rerouted. No filesystem-mailbox request or ACK
was published, and no historical request or CI dispatch was replayed. Further
promotion awaits a separate owner instruction. Full identities, results and
boundaries are in the [deployment evidence](../040-implementation-evidence/BUG-029-dev-deployment-2026-11-08.md).

## History

| Date | Change |
| --- | --- |
| 2026-11-07 | Isolated the unbounded observer mechanism and restored the two exact original requests without replay, replacement or ACK. Registered the preventive defect; no source fix or deployment was performed. |
| 2026-11-07 | Implemented bounded observer I/O and cancellation, cursor progress and disposable regressions in `14aadaa4e4c9d6e126f6fab1a71561245a2b0be4`. Mac focused/race and corrected full test/vet/build/smoke gates passed; Sandbox and Factory native source gates passed. Linux Git delivery/qualification and deployment remain separately gated. |
| 2026-11-08 (Warsaw) | Owner approved the existing HTTPS Git mirror route for BUG-029 Linux delivery, retaining TLS and exact-SHA/clean-dev/fast-forward checks. Preserved the first Linux fixture failure and delivered test-only correction `1263205`; all four hosts passed full native gates, and Mac race/latency checks passed. No release bundle, deployment or mailbox mutation. |
| 2026-11-08 | Sealed `rr-20261108-bug029-1263205` and completed four-host Dev promotion with eight functional gates and passing postflight. QAS/PRD runtime identities and mailbox routing remain unchanged; no mailbox publication or historical replay. |
