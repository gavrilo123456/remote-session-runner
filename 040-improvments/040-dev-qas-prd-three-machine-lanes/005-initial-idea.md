# Initial idea: Dev, QAS, and PRD three-machine deployment lanes

**Status:** proposed improvement; documentation only. No implementation has
started and no live service, mailbox, deployment, or credential has changed.

## Problem

Runner currently supports several named mailboxes and remote hosts from one Mac
installation, but it is still one installed code revision and one runtime
instance. Naming an execution environment `dev`, `qas`, or `prd` would label a
request, but would not isolate its binary, SQLite state, sockets, workspaces,
logs, service labels, credentials, or endpoint.

The desired workflow needs three versions to run at the same time:

- **Dev** must be a complete distributed environment, including both Linux
  machines, so a feature that coordinates two remote hosts can be developed
  and exercised before promotion.
- **QAS** must run the candidate version independently across the same Mac and
  both Linux machines.
- **PRD** must keep a stable accepted version running while Dev and QAS change.

A branch-only split or a shared Runner database would allow one lane to affect
another and would not meet that requirement.

## Goal

Introduce three fixed, concurrently runnable **release lanes**: `dev`, `qas`,
and `prd`. Each lane spans the Mac control plane and both registered Ubuntu
hosts. A source change starts on `dev`, is tested there, then the exact commit
is promoted to `qas`, and finally the same accepted commit is promoted to
`prd`.

`lane` is a deployment/release identity. It is deliberately separate from an
existing Runner execution environment such as `linux-dev` or `sandbox-dev`,
which remains a target policy and execution-account description.

## Settled naming and compatibility rule

`prd` is the release-lane identity, not a suffix on production resources. The
current unsuffixed installation is the initial PRD lane and keeps its existing
names, paths, listener, and mailbox roots. Only the new Dev and QAS instances
carry lane suffixes:

| Resource | Dev | QAS | PRD (current and unsuffixed) |
| --- | --- | --- | --- |
| Mac processes | `runner-local-dev`, `runner-locald-dev` | `runner-local-qas`, `runner-locald-qas` | `runner-local`, `runner-locald` |
| Linux systemd unit | `runnerd-dev.service` | `runnerd-qas.service` | `runnerd.service` |
| Mac runtime root | `~/Library/Application Support/RemoteSessionRunner-dev` | `~/Library/Application Support/RemoteSessionRunner-qas` | `~/Library/Application Support/RemoteSessionRunner` |
| Linux runtime root | `~/.local/share/remote-session-runner-dev` | `~/.local/share/remote-session-runner-qas` | `~/.local/share/remote-session-runner` |
| Direct HTTPS port on each Linux host | `8444` | `8445` | `8443` |

PRD still reports its lane explicitly in health, status, configuration, and
build metadata. The absence of a `-prd` suffix must never make a resource
ambiguous. This is a naming decision only; it does not authorize installation
or migration work.

## Intended topology

```text
                         Mac                     linux-host              sandbox-host
                     tomasz.walczuk                ubuntu                   ubuntu

Dev       runner-local-dev / runner-locald-dev runnerd-dev :8444       runnerd-dev :8444
QAS       runner-local-qas / runner-locald-qas runnerd-qas :8445       runnerd-qas :8445
PRD       runner-local / runner-locald        runnerd :8443           runnerd :8443
```

The same lane port is used on both Linux hosts because the hosts have separate
IP addresses. The port table is a proposed fixed mapping, not a free-form
per-request value:

| Lane | Direct HTTPS port on each Linux host |
| --- | ---: |
| `dev` | `8444` |
| `qas` | `8445` |
| `prd` | `8443` |

All direct endpoints remain public HTTPS with mandatory mTLS. The queued
mailbox route remains the restricted SSH bridge. This proposal does not add
Podman, tunnels, a reverse proxy, or free-form remote endpoints.

## Source and promotion model

```text
dev branch  -- exact tested SHA -->  qas branch  -- same accepted SHA -->  prd branch
```

- Tracked source changes are made only in the authoritative Mac `dev`
  checkout.
- `qas` and `prd` use clean deployment-only Git worktrees; no independent
  source edits or merge commits are made there.
- A promotion is fast-forward-only and records the selected commit SHA.
- Each Mac and Linux service reports its lane and immutable build revision in
  readiness/status output. A branch name alone is not proof of the installed
  code.
- QAS failure leaves PRD unchanged. The correction returns to `dev` and starts
  the promotion path again.

Git worktrees share Git objects, and all lanes reuse the existing shared Go
caches. The design avoids three full source or module-cache copies while
keeping runtime state separate.

## Lane-local routing and mailboxes

Every lane has two configured remote contexts: one for `linux-host` and one
for `sandbox-host`. A lane may contain several repository-specific mailboxes.

A mailbox has a default target and may permit a request-level override only to
the other remote context **within the same lane**. For example:

| Mailbox lane | Default | Permitted override |
| --- | --- | --- |
| Dev | `dev/linux-host` | `dev/sandbox-host` |
| QAS | `qas/linux-host` | `qas/sandbox-host` |
| PRD | `prd/linux-host` | `prd/sandbox-host` |

A request from a Dev inbox cannot choose a QAS or PRD target. The same rule
applies in every direction. Request/response/event/ACK paths, idempotency
identity, and cleanup are lane-local.

The Mac control service exists in every lane. Whether QAS or PRD mailboxes may
also execute commands locally on the Mac is a policy decision for the detailed
design; remote targets remain available in all three lanes.

## Required isolation

Each lane must have its own configured, owner-controlled resources:

- Mac service root, config, SQLite database, Unix sockets, logs, workspaces,
  installed binary, LaunchAgent labels, and mailbox roots.
- Linux service root, config, SQLite database, Unix socket, logs, workspaces,
  installed binary, systemd unit name, and HTTPS listener.
- mTLS endpoint/client identities and controller authorization that prevent a
  Dev or QAS client from controlling a PRD service.
- Queued-SSH bridge identity, private socket, forced-command route, and
  controller map.
- Direct endpoint/profile names and health/status identity.

One mailbox root is watched by exactly one lane. No lane may read another
lane's SQLite database, socket, workspace, bridge socket, or response tree.

## Compatibility and migration direction

The current live installation remains operational as the initial PRD instance.
It keeps the unsuffixed service labels, runtime roots, port `8443`, and existing
mailbox locations. Dev and QAS are introduced as fresh isolated instances with
their `-dev` and `-qas` names. Existing mailboxes must not be silently moved or
retargeted. A specific production mailbox may move to Dev or QAS only after the
new lane has passed end-to-end acceptance and the migration is explicitly
performed.

The detailed design must provide a reversible, evidence-backed migration for
runtime roots, existing retained mailbox artifacts, active work, and installed
service labels.

## Constraints and practical limits

- This creates process and data isolation, not a hard operating-system security
  boundary: Mac processes still run as `tomasz.walczuk`, and the three Linux
  lane processes on each host run as `ubuntu`.
- Each Linux host will run three Runner instances. Disk capacity, memory,
  listener/firewall rules, retention, workspace growth, and log limits require
  a measured pre-install gate. The sandbox host was recently reported near
  full and must be checked before this extension is installed there.
- QAS and PRD must test both Linux architectures presently in use: the
  sandbox is ARM64 and the primary Linux host is AMD64.
- Direct mTLS, queued bridge, mailbox marker-last publication, durable
  idempotency, event replay, ACKs, and recovery semantics remain unchanged
  within each lane.
- No request may provide a free-form lane, host, account, port, certificate,
  or filesystem path.

## Non-goals

- Maintaining three independent codebases or copying source changes directly
  to Linux hosts.
- Treating a YAML environment label as sufficient deployment isolation.
- Sharing state, sockets, mailboxes, credentials, or service labels between
  lanes.
- Adding containers, tunnels, reverse proxies, or a general-purpose remote
  command channel.
- Claiming hostile-code isolation while all lanes retain the same operating
  system accounts.

## Acceptance criteria for a later implementation plan

- Dev can submit independent remote work to both Dev Linux instances and
  retain correct target, output, events, and ACKs.
- QAS and PRD can run at the same time as Dev without port, socket, database,
  mailbox, workspace, log, or service-label collisions.
- A QAS deployment or restart cannot alter a PRD process, data store, mailbox,
  credential, listener, or build revision.
- Each remote service accepts only its lane's authorized controller identity;
  cross-lane direct and queued requests fail before command creation.
- A lane reports the expected lane and build SHA on the Mac and on both Linux
  hosts before its acceptance tests start.
- Fast-forward-only promotion proves the exact Dev-to-QAS-to-PRD SHA chain.
- Existing PRD service and mailbox state remain available without lost or duplicate
  work, and rollback of an incomplete migration is documented and tested.
- Per-lane capacity and retention controls prevent one lane's generated output
  from exhausting the shared host disk.

## Open design questions

1. What exact worktree paths best preserve the existing PRD state while making
   Dev and QAS paths unmistakable?
2. Should QAS and PRD permit any Mac-local execution, or should their mailboxes
   be remote-only from the first release?
3. What issuer/identity arrangement gives straightforward per-lane mTLS
   authorization while keeping credential rotation manageable?
4. What per-lane disk, output-retention, workspace-retention, and log limits
   fit both Linux hosts after measured capacity checks?
5. How should service installation validate the intended lane, Git branch, and
   exact SHA without relying on the current universal `dev`-branch check?
6. What small operator command set best promotes, deploys, verifies, rolls
   back, and reports the three lanes without allowing unsafe cross-lane action?

The detailed design and phased implementation plan below remain placeholders
until these questions are resolved against the current service, mailbox,
recovery, and deployment contracts.
