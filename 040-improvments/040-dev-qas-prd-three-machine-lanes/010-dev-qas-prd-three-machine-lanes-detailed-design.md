# Detailed design: Dev, QAS, and PRD three-machine deployment lanes

**Status:** placeholder — not designed or approved for implementation.

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
