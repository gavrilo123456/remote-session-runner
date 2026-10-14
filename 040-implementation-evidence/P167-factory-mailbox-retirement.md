# P167 — Factory direct-mailbox replacement and legacy retirement

**Status:** PREPARED — the direct replacement has passed its own native
mailbox acceptance; logical retirement and physical removal of the legacy tree
remain pending this controlled transition.

## Scope

P157 factory-sandbox accepted the original Factory mailbox
`agentic-software-factory-runner` at
`/Users/tomasz.walczuk/projects/agentic-software-factory/runner/tmp/mailbox-`.
That historical acceptance is preserved in
[P157 factory-sandbox](P157-factory-sandbox.md); it is not rewritten as proof
for a new path.

The replacement is the separately registered
`agentic-software-factory-runner-direct` mailbox at
`/Users/tomasz.walczuk/projects/agentic-software-factory/runner/mailbox-`.
It retains the `agentic-software-factory` alias, defaults to
`ubuntu-factory-sandbox`, and allows `mac-local` as its only override. This
record covers retirement of the old ID/root only; it does not change the
accepted Factory host, queued bridge, or direct mTLS endpoint.

## Direct-root acceptance already recorded

The controlled Mac installation at source revision
`81b4d8d4e9df830e4b77dd02fb4c7462529ea3bf` registered the direct root and
created it and its five child directories as selected-user-owned `0700`
directories. The installer postflight reported ready Router and local executor
health with zero queued work and zero mailbox backlog.

Two direct-root native mailbox controls reached a complete successful terminal
response and had their exact ACK pairs durably consumed:

| Request | Result |
| --- | --- |
| `req-factory-direct-18de82ebddd72ce0` | Complete success and acknowledged; an assertion in the temporary test harness ran after the successful response and was corrected without replaying the request. |
| `req-factory-direct-18de830834192808` | Fresh corrected control; complete success and acknowledged. |

These controls prove the direct configured root's marker-last request,
terminal response, event projection, and ACK path. They do not retire the
legacy root or authorize deletion of its parent directory.

The opt-in installed Factory control remains named
`make test-p157-factory-sandbox-host` because the host-route gate is P157, but
its configured mailbox fixture now resolves
`agentic-software-factory-runner-direct` and the direct root above. The test
name is not evidence that the original legacy root was reused.

## Legacy-retirement boundary

Before retirement, a fresh read-only preflight found the legacy root still
registered, with no inbox, ACK, or diagnostic inputs; its four retained traces
were terminal and acknowledged. Its response/event retention windows are not a
reason to bypass the registry. The logical retirement must use the reviewed
candidate that retains the direct mailbox and omits only
`agentic-software-factory-runner`, then name that exact ID with
`--retire-mailbox`.

Retirement is a cooperative ingress handoff. Before the installer runs, stop
every process that can publish to the legacy root, including automation,
interactive clients, and direct-workspace publishers. Quiescing Runner's
services does not stop an independent file writer. After service quiescence
and the zero-work database preflight, the installer takes the legacy root's
exclusive fence lock, installs and syncs an owner-only sentinel through a
descriptor-safe root handle, and descriptor-scans `inbox` and `acks` while the
lock remains held. Both directories must be completely empty: any entry is a
retirement refusal. Retained acknowledged history in `outbox` or `events` may
remain; neither is ingress residue.

A cooperating native client takes the shared fence lock and checks the
sentinel immediately before publishing a marker-last request or ACK pair. It
then fails locally instead of writing the retired root. The fence is not an
adversarial isolation boundary: a same-UID malicious writer or a process with
an already retained directory file descriptor can evade that cooperation.
Those writers must be stopped before the fence and must remain quiescent.

There is one pre-boundary safe atomic-unwind refusal. If this handoff creates a
**fresh** fence and its frozen `inbox` or `acks` scan finds residue before the
post-fence authority open, migration, or retirement tombstone, Runner restores
both ingress directories to `0700`, syncs them, removes and syncs the sentinel
while it still holds the exclusive lock, and returns a distinct fresh-residue
result. Only after that complete unwind may the installer restore the prior
active configuration and LaunchAgents. No retirement is recorded in that case;
the residue must be settled and the read-only preflight repeated. This record
does not claim that this exception has occurred.

Every other retirement activation failure remains no-rollback candidate repair.
A preexisting fence, any failed or incomplete fresh-fence unwind, fence setup
failure, and every failure after the post-fence authority open, migration, or
tombstone leave the candidate state and any durable fence in place. Do not
restore the old configuration or its LaunchAgents after those failures,
including a later durable-state, registration, path-preparation, or startup
failure: the candidate is the safe repair state for publishers that have
observed the sentinel. Repeating the same omitted-ID retirement is idempotent
for a post-boundary repair; reusing the retired ID/root in a candidate remains
invalid.

At creation of this record, the legacy directory
`/Users/tomasz.walczuk/projects/agentic-software-factory/runner/tmp/mailbox-`
has **not** been physically removed. The fence and tombstone never delete the
legacy root. Complete this record only after the candidate source is delivered
through primary Gitea and the GitHub mirror, the Mac installer reports
successful logical retirement, the active direct root and services have a
fresh postflight, and a separately authorized exact-tree removal has verified
the old root identity and fresh publisher quiescence. Preserve any required
retained evidence first. Do not remove `runner/tmp`, change SQLite, or alter
the original P157 evidence.
