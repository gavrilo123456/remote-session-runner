# Detailed phased implementation plan: multiple inboxes and named remote machines

**Status:** completed historical implementation plan. The [initial
idea](005-initial-idea.md) and [detailed
design](010-multiple-inboxes-and-remote-machines-detailed-design.md) preserve
the delivered contract. The original initial design, detailed design, and
detailed phased implementation plan remain authoritative wherever this
extension does not change them.

**Target:** retain the tested Mac user `tomasz.walczuk`, current Ubuntu user
`ubuntu`, public direct mTLS route, and restricted queued SSH bridge. This
adds explicitly selected named remote profiles and several Mac mailboxes; it
does not add containers, tunnels, arbitrary accounts, a scheduler that chooses
hosts, source materialization, or fallback between targets.

## Completed delivery

| Phase | Delivered result | Evidence |
| --- | --- | --- |
| P150 | V2 configuration registries | [P150](../../040-implementation-evidence/P150.md) |
| P151 | Profile-aware remote routing and direct endpoint binding | [P151](../../040-implementation-evidence/P151.md) |
| P152 | Inbox-scoped durable exchange identity | [P152](../../040-implementation-evidence/P152.md) |
| P153 | Inbox default and complete-override resolution | [P153](../../040-implementation-evidence/P153.md) |
| P154 | One runtime per configured inbox | [P154](../../040-implementation-evidence/P154.md) |
| P155/P156 | Current-host deployment and post-implementation documentation | [P155](../../040-implementation-evidence/P155.md), [P156](../../040-implementation-evidence/P156.md) |
| P157 | Separate `sandbox-host` acceptance | [P157](../../040-implementation-evidence/P157-sandbox-host.md) |
| P158 | Owner-safe external `slidestud-io` mailbox | [P158](../../040-implementation-evidence/P158-slidestud-external-mailbox.md) |
| P159 | Direct workspace-file `0644` ingress/ACK compatibility | [P159](../../040-implementation-evidence/P159.md) |

The phase descriptions below are retained as the record of their required
serial gates. They are not authorization to rerun a completed phase. Future
inbox or host changes begin from the current design, configuration, and
per-host P157 boundary.

## 1. Mandatory per-phase protocol

Every phase in this plan follows the evidence and safety rules in the original
implementation plan. The rules below make those requirements explicit for
this extension.

### 1.1 Fresh context before each phase

Before editing code for `P150` or any later phase, read through end-of-file:

1. the original initial design, detailed design, and detailed phased plan;
2. this extension's initial idea, detailed design, and this plan;
3. root `AGENTS.md`, current `README.md`, preimplementation decisions, the
   prior completed phase evidence and diff, and current Git status/HEAD; and
4. every current source, test, schema, migration, configuration, deployment,
   and documentation file relevant to that phase.

Search first and open the selected files completely. A conversation summary,
prior phase read, or test output does not substitute for fresh reads. If the
context is compacted, a design changes, or the phase resumes after interruption,
repeat the reads and update the evidence before continuing.

Before coding, create or update `040-implementation-evidence/Pnnn.md` with
the Mac pre-phase HEAD, relevant design and plan blob revisions, complete
file-read inventory, execution machine, prerequisites, planned tests, and
unresolved assumptions. A material design or plan amendment is committed
separately before affected implementation resumes.

### 1.2 Serial completion, tests, and delivery

The source sequence is strictly `P150 → P151 → P152 → P153 → P154 → P155 →
P156`. Do not start phase N+1 until phase N has all of the following:

- its bounded deliverable and focused automated assertions;
- `make test`, `go vet ./...`, and `git diff --check` on the Mac; race tests
  when the phase changes concurrency, lifecycle, storage, or mailbox cycles;
- every named real-process or host test required by that phase, with machine,
  account, profile, command, and result recorded;
- an inspected diff and evidence record marked `PASS`; and
- one non-empty `phase(Pnnn): ...` commit, a clean Mac worktree, a push to
  GitHub `dev` using the prescribed Mac key, then a clean Ubuntu `dev`
  fast-forward pull using the prescribed Ubuntu key and matching commit proof
  before remote validation or the next phase.

All versioned work is made in the Mac checkout. Ubuntu receives only the
fast-forwarded Git commit; source files are never copied or edited there.
Shared Mac Go caches are reused. A failed or unavailable required gate stops
the phase. A fixture, TLS transport probe, or a successful current host does
not pass an untested future physical host.

### 1.3 Continuing scope and evidence limits

`linux-host` and `sandbox-host` have separate live acceptance evidence; this
does not transfer to a future configured profile. Direct public mTLS health
proves the Runner API route only; it does not prove queued mailbox delivery.
The user-approved software-process-crash limitation remains in force: physical
power-loss durability is unverified until coordinated physical tests pass, and
no later work may relabel it as proven.

## 2. Serial implementation phases

| Phase | Bounded deliverable | Focused exit gate |
| --- | --- | --- |
| `P150` | Add backward-compatible configuration v2 parsing and validation for named execution contexts, remote host profiles, endpoint profiles, and mailbox definitions. Version 1 is exposed as the implicit `default` inbox/current host without moving existing files. | Config fixtures cover valid v1 and v2, unsafe path/secret/reference rejection, duplicate endpoint/context/root rejection, and invalid mailbox default/allow-list rejection. |
| `P151` | Replace the singleton remote caller with a profile-aware pinned SSH caller resolver. Route dispatch, recovery reads, event replay, and health by the immutable stored remote target profile. Add named direct mTLS endpoint profiles bound to target profiles. | Hermetic two-caller fixture proves strict A/B selection, no fallback, recovery reads the original profile, per-profile health, and endpoint/profile mismatch rejection. |
| `P152` | Add durable mailbox namespace fields and scoped internal exchange/idempotency identity. Migrate retained existing rows to `default`; scope response publication, event references, ACKs, recovery, and cleanup. | Prior-schema migration fixture and two-inbox same-client-ID/key fixture. No cross-inbox receipt, response, event, ACK, retry, projection, or cleanup effect. |
| `P153` | Resolve each mailbox's default context and validate complete explicit overrides for `run` and `create_session`. Persist the resolved selection and add additive mailbox response/audit fields. | Default, allowed override, partial override, target/environment mismatch, repository-scope metadata, and session-immutability fixtures. Rejected selection creates no intent or remote work. |
| `P154` | Compose one mailbox runtime per configured inbox. Update Mac installation/config creation, owner-mode and symlink checks, diagnostics, and configuration examples without changing the v1 default behavior. | Race-enabled multi-inbox cycle test, legacy default regression, owner-mode/symlink test, installer/config validation, and full hermetic suite. |
| `P155` | Deploy the completed extension to the current Mac and current Ubuntu host. Prove the legacy default inbox plus a non-default inbox, including default local execution and an allowed queued remote override to the existing `linux-host`. | Current Ubuntu zero-active-work/status and permanent bridge gates; Mac real-process gate; two end-to-end mailbox requests with distinct roots, event/ACK isolation, and no source copied to Ubuntu. |
| `P156` | Update post-implementation documentation, diagrams, configuration reference, setup and upgrade runbooks, CLI and mailbox user guides, operations/troubleshooting, and current-host evidence index. | Review every documented path, configuration field, example command, target-selection rule, and architecture diagram against the committed implementation. Clearly identify current-host evidence and the per-host P157 requirement. |

## 3. Additional physical-host onboarding

`P157` is a repeatable **per-host** gate after P155/P156 whenever the user
supplies another Ubuntu machine. It is not inferred from any earlier phase.
For each named remote profile, install its `runnerd` configuration and service,
host-key pin and restricted bridge or direct mTLS materials, then prove account
`ubuntu`, listener/identity, route selection, and an end-to-end request to
that specific profile. Record a separate P157 evidence file keyed by the host
profile. A host that is unavailable is `NOT RUN`, never `PASS`.

## 4. External mailbox roots and P158

`P158` extends the completed multi-inbox implementation after `P157`. It
keeps the legacy default and service-root mailbox layout, while permitting a
non-default mailbox at any owner-safe absolute location outside the service
root. This is a mailbox-ingress change, not a new host onboarding gate:
`sandbox.env` remains only the bootstrap SSH alias, and the configured Runner
default must name the already accepted `ubuntu-sandbox` context
(`sandbox-dev` / `remote/sandbox-host`).

| Phase | Bounded deliverable | Focused exit gate |
| --- | --- | --- |
| `P158` | Add fail-closed support for owner-safe external mailbox roots, a non-mutating preflight before service quiescence, and add `slidestud-io` at `/Users/tomasz.walczuk/projects/slidestud.io/tmp/mailbox-` with default `ubuntu-sandbox`. | Config and runtime tests reject non-absolute, normalized-alias/nested, service-root-escape, symlinked, missing-parent, and group/other-writable external paths; they preserve existing roots and do not change external ancestors. Descriptor-relative preflight happens before quiescence, while durable candidate registration happens before any new external root is created. The activated inbox completes a native `mailboxclient` request with no selection pair, resolves `sandbox-dev` / `remote/sandbox-host`, returns complete untruncated output/events, accepts the exact ACK, and finishes with sandbox P128 zero active work. Direct mTLS is not mailbox evidence. |

## 5. Workspace-compatible direct-file ingress and P159

`P159` follows the completed P158 external-mailbox gate. It permits a
workspace file integration to create a complete, marker-last request or ACK
pair at exact `0644` inside a configured mailbox's owner-only `0700` root and
children. It preserves native `mailboxclient` publication at `0600` and keeps
Runner-produced outbox and event files at `0600`. This is ingress compatibility
for file-only workspace integrations; it is not a claim that direct file
creation has the native client's exclusive-create, no-follow, sync, or
crash-durability behavior.

| Phase | Bounded deliverable | Focused exit gate |
| --- | --- | --- |
| `P159` | Accept exact `0644` alongside exact `0600` for client-published `inbox` and `acks` JSON/`.ready` pairs, while retaining exact `0600` validation for outbox/event projections; update cleanup and backlog accounting and document the direct workspace-file path. | Hermetic tests accept `0644` request and ACK pairs, retain native `0600` behavior, reject `0640`, `0664`, and symlinks, clean an old `0644` draft, count an eligible `0644` marker, and keep response/event files `0600`. After Mac source delivery and both Ubuntu fast-forwards, the active `slidestud-io` external root completes a direct `0644` `uname -a` request using its default `ubuntu-sandbox` route, returns complete untruncated output/events, accepts a direct `0644` exact ACK, and ends with sandbox P128 zero active work. |

## 6. Completion and handoff

The configurable-inbox extension is ready for the current controlled PoC only
after P150--P156 and any later applicable phase such as P158 and P159 pass in order,
each has a phase commit and GitHub/Ubuntu handoff where required, the current
Mac and Ubuntu evidence is current, and the documentation describes the actual
installed behavior. Additional remote machines are ready only after their own
P157 gate. The handoff continues to state that physical power-loss survival is
unverified unless coordinated physical evidence later changes that fact.
