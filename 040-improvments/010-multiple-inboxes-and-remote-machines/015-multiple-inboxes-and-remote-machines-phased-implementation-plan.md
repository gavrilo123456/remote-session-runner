# Phased implementation plan: multiple inboxes and remote machines

This plan extends the completed Runner PoC after `P149`. Each phase follows the
repository's existing evidence, fresh-context, Mac-first commit/push, and
Ubuntu fast-forward workflow. A phase does not pass because configuration or a
fake fixture exists; named host gates require the corresponding host.

| Phase | Deliverable | Required gate |
| --- | --- | --- |
| `P150` | Implement backward-compatible configuration v2 parsing and validation for named execution contexts, remote hosts, endpoint profiles, and mailbox definitions. Retain v1 as the implicit `default` inbox/current host. | Config fixtures: valid v1, valid v2, unsafe path/secret/reference rejection, duplicate names/roots, invalid default or allow-list rejection. |
| `P151` | Implement named remote host routing: a profile-aware pinned SSH caller resolver, per-profile health, and named direct CLI endpoint profiles bound to target profiles. | Hermetic two-caller fixture proves strict A/B selection, no fallback, recovery reads the original profile, and direct endpoint/profile mismatch rejection. |
| `P152` | Add durable mailbox namespace fields and scoped internal exchange/idempotency identity. Migrate existing rows to `default`; scope acknowledgements, projections, event references, recovery, and cleanup. | Prior-schema migration fixture plus two-inbox same-client-ID/key fixture. No cross-inbox response, event, ACK, retry, or cleanup effect. |
| `P153` | Add per-inbox default execution resolution and explicit complete override handling for `run` and `create_session`; add additive response audit fields. | Default, allowed override, partial override, target/environment mismatch, and session immutability fixtures. No intent is created for rejected selection. |
| `P154` | Compose multiple mailbox runtimes in `runner-local`; update installer/config examples, configuration and mailbox documentation, and operator diagnostics. | Race-enabled multi-inbox cycle test, legacy default regression, owner-mode/symlink checks, and full hermetic suite. |
| `P155` | Deploy the completed extension to the current Mac and Ubuntu host. Prove the legacy default inbox and a non-default inbox, including default local execution and an allowed queued remote override to the current `linux-host`. | Mac real-process test; current Ubuntu bridge/status gate; two end-to-end mailbox requests with separate inbox roots; ACK/event isolation proof. |
| `P156` | Onboard and validate each additional physical Ubuntu host supplied for this PoC. | Per-host `runnerd`, bridge or mTLS, pinned-host-key, account, and end-to-end target-selection evidence. A host that is not available is `NOT RUN`, never `PASS`. |

## Sequence constraints

- `P150` through `P154` are source phases. Each receives its own evidence file
  and non-empty phase commit before the next starts.
- `P155` starts only after all source phases are pushed from the Mac and
  fast-forwarded onto the current Ubuntu host at the same commit.
- `P156` is repeated once for each new physical remote machine. Its absence
  does not invalidate the tested current host, but it prevents a claim that an
  unprovisioned host is ready.
- Repository scope remains metadata throughout this plan. Source checkout or
  materialization work requires a separate design and plan.
