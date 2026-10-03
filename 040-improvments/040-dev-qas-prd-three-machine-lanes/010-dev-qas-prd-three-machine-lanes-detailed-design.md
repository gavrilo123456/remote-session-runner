# Detailed design: Dev, QAS, and PRD three-machine deployment lanes

**Status:** placeholder — not designed or approved for implementation. The
production naming baseline below is settled; all other design choices remain
open.

## Settled production naming baseline

The existing unsuffixed installation is PRD. `prd` identifies the release lane
in status and configuration; it is not appended to production service or path
names.

| Lane | Mac processes | Linux unit | Runtime-root form | HTTPS port |
| --- | --- | --- | --- | ---: |
| Dev | `runner-local-dev`, `runner-locald-dev` | `runnerd-dev.service` | `.../remote-session-runner-dev` | `8444` |
| QAS | `runner-local-qas`, `runner-locald-qas` | `runnerd-qas.service` | `.../remote-session-runner-qas` | `8445` |
| PRD | `runner-local`, `runner-locald` | `runnerd.service` | existing unsuffixed `.../remote-session-runner` | `8443` |

No later design or phase may rename, repurpose, or move the current unsuffixed
PRD service, listener, state, or mailbox locations as part of creating Dev or
QAS. Any explicit migration must preserve this compatibility rule.

This document will define the fixed lane model, runtime-root namespace,
branch/worktree promotion rules, service and endpoint naming, port allocation,
mTLS and queued-bridge authorization, mailbox routing constraints, capacity
limits, migration, rollback, observability, and automated acceptance tests for
[the initial idea](005-initial-idea.md).

It must preserve the Mac-authoritative Git workflow, file-mailbox protocol,
durable lifecycle/recovery rules, direct mTLS requirement, and separate
local/remote execution semantics.

No code, configuration, deployment behavior, service, mailbox, firewall rule,
or credential is authorized by this placeholder.
