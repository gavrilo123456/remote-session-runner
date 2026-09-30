# Detailed phased implementation plan: multiple inboxes and named remote machines

**Status:** approved implementation plan. This plan extends the completed
Remote Session Runner PoC after P149. The [initial idea](005-initial-idea.md)
defines the user goal and the [detailed design](010-multiple-inboxes-and-remote-machines-detailed-design.md)
defines the extension contract. The original initial design, detailed design,
and detailed phased implementation plan remain authoritative wherever this
extension does not change them.

**Target:** retain the tested Mac user `tomasz.walczuk`, current Ubuntu user
`ubuntu`, public direct mTLS route, and restricted queued SSH bridge. This
adds explicitly selected named remote profiles and several Mac mailboxes; it
does not add containers, tunnels, arbitrary accounts, a scheduler that chooses
hosts, source materialization, or fallback between targets.

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

### 1.3 Scope and evidence limits

The current Ubuntu host is the first and only host with live acceptance
evidence. A second configured profile is a configuration/fake-route fixture
until its own P157 host gate passes. Direct public mTLS health proves the
Runner API route only; it does not prove queued mailbox delivery. The
user-approved software-process-crash limitation remains in force: physical
power-loss durability is unverified until coordinated physical tests pass, and
no extension phase may relabel it as proven.

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

## 4. Completion and handoff

The configurable-inbox extension is ready for the current controlled PoC only
after P150--P156 pass in order, each has a phase commit and GitHub/Ubuntu
handoff where required, the current Mac and Ubuntu evidence is current, and
the documentation describes the actual installed behavior. Additional remote
machines are ready only after their own P157 gate. The handoff continues to
state that physical power-loss survival is unverified unless coordinated
physical evidence later changes that fact.
