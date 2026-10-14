# P157 — `factory-sandbox` onboarding and factory mailbox acceptance

**Status:** PASS. This record accepts one additional Ubuntu profile and its
configured external mailbox. It does not transfer acceptance to any other
profile, turn direct mTLS into mailbox evidence, or prove physical power-loss
recovery.

## Accepted identity and live revision

| Item | Accepted value |
| --- | --- |
| Bootstrap machine alias | `factory-sandbox` |
| Service account and host | `ubuntu@oracle-waclaw-dziwulski-ubuntu-micro-01` |
| Host platform | Ubuntu 26.04 LTS, `x86_64` |
| Runner profile / environment / queued context | `factory-sandbox` / `factory-sandbox-dev` / `ubuntu-factory-sandbox` |
| Public direct endpoint | `factory-sandbox-poc` → `https://145.241.255.66:8443` |
| Private Runner listener | `10.0.0.30:8443` |
| Installed service revision during acceptance | `f21ea09b58d3521847fc53b9054484017a20e8e6` |

The host has its own owner-only Runner configuration, state database, service
socket, and direct/queued credentials. The direct mTLS client material is
separate from the queued bridge's host-key pin and dispatcher identity, and
both are distinct from the materials for the other accepted profiles. No
private key, certificate body, or secret value is recorded here.

`runnerd.service` was active as `ubuntu`; direct Runner readiness passed at the
public endpoint with mandatory mTLS, and the profile-specific restricted bridge
read proof reported ready. Those are different transport paths: direct mTLS
does not establish mailbox delivery, and bridge readiness does not establish a
direct client path.

The installed service revision above describes the live acceptance point. This
evidence document has no closeout commit assigned yet. Its source/evidence
delivery must be recorded only after the normal Mac primary-Gitea push, GitHub
mirror verification, and clean Ubuntu fast-forward gates; that later commit
must not be confused with the live installed `f21…` revision.

## External mailbox policy and activation

| Item | Accepted value |
| --- | --- |
| Mailbox ID | `agentic-software-factory-runner` |
| Mailbox root | `/Users/tomasz.walczuk/projects/agentic-software-factory/runner/tmp/mailbox-` |
| Repository alias | `agentic-software-factory` |
| Default execution | `ubuntu-factory-sandbox` |
| Allowed execution | `ubuntu-factory-sandbox`, `mac-local` |
| Omitted-target resolution | `factory-sandbox-dev` / `remote/factory-sandbox` with `inbox_default` |

The reviewed Mac candidate validated without mutating the external mailbox
tree. Its guarded activation registered the complete candidate mailbox set
before it created the new root. It then created the root and exactly these
children as real directories owned by `tomasz.walczuk` at mode `0700`:

```text
mailbox-/
├── inbox/
├── outbox/
├── events/
├── acks/
└── diagnostics/
```

The safe pre-existing parent is
`/Users/tomasz.walczuk/projects/agentic-software-factory/runner/tmp`.
It is an ancestor only; the configured mailbox root is `tmp/mailbox-`.
The project checkout retains a local runtime exclusion for that mailbox tree,
so runtime exchanges do not become project source changes.

## Native end-to-end gate

The opt-in Mac gate was:

```sh
make test-p157-factory-sandbox-host
```

It used the native `mailboxclient` to publish one marker-last `run` request
without `environment` or `execution_target`. Its harmless script was:

```sh
printf 'FACTORY_SANDBOX_MAILBOX_OK\n'; id -un; hostname; uname -m
```

The resulting identifiers and terminal proof were:

| Item | Accepted value |
| --- | --- |
| Request | `req-p157-factory-sandbox-18de80a1747f87e8` |
| Job | `job-aaf87b7088f0008f87dbc3874d3edd45` |
| Session | `sess-bc2a0826c0e915088683c3841a183acb` |
| Command | `cmd-8f8574c906f18a8ca2a765bc1011a936` |
| Selection | `inbox_default` → `factory-sandbox-dev` / `remote/factory-sandbox` |
| Terminal response | Revision 2; `request_state: complete`; `command_state: succeeded`; exit code `0`; complete, nontruncated output |
| Event proof | Read through final cursor `5` and matched the fixed stdout sequence |
| ACK and cleanup | Exact ACK consumed; matching input pair consumed |

The terminal output matched the fixed marker, `ubuntu`,
`oracle-waclaw-dziwulski-ubuntu-micro-01`, and `x86_64`. The gate's pre- and
post-request Mac checks required ready factory routing with zero active session
and command slots, queued commands/intents, and mailbox backlog. A separate
post-gate Mac preflight found no resumable work, and the direct-mTLS factory
host health reported zero active slots, queued work, storage/cleanup failures,
and mailbox backlog.

The final source-gate rerun after health and continuity-test hardening passed
with request `req-p157-factory-sandbox-18de81711c4e9a48` and command
`cmd-2829eeb6bfe6682b118a8a5c7f83fa42`; it repeated the same omitted-target,
terminal-event, exact-ACK, and zero-work assertions.

## Narrow direct-mTLS ingress condition

The host firewall admits public direct-mTLS traffic on port 8443 only from the
two observed authorized Mac egress sources:

- `81.95.203.65/32`
- `31.61.247.138/32`

This is an operational ingress constraint for the `factory-sandbox-poc` health
and direct-client route. Revalidate it if the authorized Mac egress network
changes; it does not expose a general public Runner listener.

## Boundary

This P157 result proves the named host service, its direct mTLS readiness, its
restricted queued bridge, and one terminal native mailbox exchange through the
new inbox default. It does not accept a different host, retry historical work,
or expand the P143 software-process-crash evidence into physical power-loss
durability.
