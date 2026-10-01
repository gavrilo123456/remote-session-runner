# Current-host evidence index

This page indexes the accepted deployment evidence for the controlled PoC. It
is a record of completed gates, not a live-health substitute. Run the checks in
[operations](operations.md) before operating a service today.

## Accepted current topology

| Component | Accepted identity | Evidence |
| --- | --- | --- |
| Mac services | `tomasz.walczuk` at `/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner` | P155 real-process two-inbox test, P158 external-mailbox activation, P159 direct-workspace mailbox test, P166 safe malformed-ingress diagnostic acceptance, private socket/readiness checks, and zero-work evidence. |
| Current Ubuntu host | `ubuntu@oracle-yuta-konopka-ubuntu-micro-02` / `linux-host` | P155 `runnerd.service`, listener `10.0.0.200:8443`, zero-work P128 checks, and permanent bridge status. |
| Sandbox Ubuntu host | `ubuntu@oracle-gustaw-janecki-ubuntu-flex-02` / `sandbox-host` | P157 `runnerd.service`, listener `10.0.0.14:8443`, zero-work P128 checks, and permanent bridge status after the authorized repair. |
| Current direct route | `linux-poc` → `https://129.151.232.40:8443` | P155 Runner application mTLS health and direct CLI evidence, separate from mailbox evidence. |
| Sandbox direct route | `sandbox-poc` → `https://132.226.205.205:8443` | P157 Runner application mTLS readiness (`HTTP 200`), separate from the queued mailbox request. |
| Current queued route | `ubuntu-current` → `remote/linux-host` | P155 permanent restricted bridge and an end-to-end analytics mailbox override. |
| Sandbox queued route | `ubuntu-sandbox` → `remote/sandbox-host` | P157 permanent restricted bridge and native end-to-end analytics mailbox request; P158 external SlideStudio default and P159 direct-workspace-file default. |

The associated source and evidence closeout are:

- P155 implementation: `359979b39995121c2f8e1b118f5c9160be1e1bff`
- P155 legacy-schema repair: `a3097a02d3aff6d2231b08f72ba768463a9933c0`
- P155 evidence closeout: `e93d55e9220338b25e1f7417ca296c9e04530b80`
- Detailed record: [P155 evidence](../040-implementation-evidence/P155.md)
- P157 repair and final host-gate revision:
  `8873852ddc9ab33093c105371de93a3695d99b89`
- Detailed record: [P157 sandbox-host evidence](../040-implementation-evidence/P157-sandbox-host.md)
- P158 source and host-gate revision:
  `ce74e368f2aa40f9db90d1bb049f96fd533c5753`
- Detailed record: [P158 SlideStudio external-mailbox evidence](../040-implementation-evidence/P158-slidestud-external-mailbox.md)
- P159 source and host-gate revision:
  `ee140ec5e4eefcdd5716a356bbbfec0f62c014b7`
- Detailed record: [P159 workspace-compatible mailbox evidence](../040-implementation-evidence/P159.md)
- P165 malformed-ingress source: `e8171cb0430309b2d6284043762c65d7c8531980`
- P165 evidence closeout: `b8673580f02d834d26bd33c91fda54180524f1d5`
- P166 source and primary-Ubuntu handoff:
  `50e5fe3f11bb2a4317df3ab830b4fbce828647b3`
- Detailed record: [P166 malformed-ingress acceptance](../040-implementation-evidence/P166.md)

## What P155 proved

At the P155 deployment, the installed Mac V2 policy had these roots:

| Inbox | Root | Default | Allowed explicit contexts |
| --- | --- | --- | --- |
| `default` | `.../RemoteSessionRunner/mailbox` | `mac-local` | `mac-local`, `ubuntu-current` |
| `analytics` | `.../RemoteSessionRunner/mailboxes/analytics` | `mac-local` | `mac-local`, `ubuntu-current` |

The native P155 mailbox test used the same visible request and idempotency
identities in both roots. It proved namespace isolation and these outcomes:

1. `default` omitted the selection pair, resolved to `mac-dev` /
   `local/mac-workstation`, and ran as `tomasz.walczuk`.
2. `analytics` supplied the complete allowed `linux-dev` /
   `remote/linux-host` pair, resolved as `request_override`, and ran through
   the restricted queued bridge as `ubuntu`.
3. The roots produced different job/session/command IDs, isolated event files,
   complete untruncated terminal output, and isolated ACK cleanup.
4. Post-test Mac and Ubuntu zero-work checks passed. The bridge status was
   ready for the deployed P155 source revision.

The analytics proof is queued mailbox evidence. It did not use direct mTLS as
a substitute.

## What P157 proved

P157 is a separate host gate. It accepted `sandbox.env` as
`ubuntu@132.226.205.205`, with the Runner profile `sandbox-host`, environment
`sandbox-dev`, queued context `ubuntu-sandbox`, public direct endpoint
`sandbox-poc`, and host listener `10.0.0.14:8443`.

The final gate ran after the authorized certificate and installer-cache repair
on source revision `8873852ddc9ab33093c105371de93a3695d99b89`. It proved:

1. the sandbox service runs as `ubuntu` on
   `oracle-gustaw-janecki-ubuntu-flex-02` (`aarch64`), with its private socket
   and the expected listener;
2. TLS transport and the Runner `/health/ready` application response pass over
   mandatory mTLS at `https://132.226.205.205:8443`;
3. the profile-specific restricted SSH bridge is ready and the Mac router
   reports both `linux-host` and `sandbox-host` ready; and
4. `make test-p157-sandbox-host` published a native `analytics` mailbox
   request for `sandbox-dev` / `remote/sandbox-host`, verified its expected
   `ubuntu`, hostname, and architecture output, read events, and wrote the
   ACK.

At the P157 gate, the policy limited `default` to `mac-local` and
`ubuntu-current`, while `analytics` permitted the explicit `ubuntu-sandbox`
override. P157 did not make the sandbox an automatic destination or fallback.

## What P158 proved

P158 added the owner-safe external root
`/Users/tomasz.walczuk/projects/slidestud.io/tmp/mailbox-` as the
`slidestud-io` namespace. Its repository alias is `slidestud-io`, its default
is `ubuntu-sandbox` (`sandbox-dev` / `remote/sandbox-host`), and its allowed
contexts also include `mac-local` and `ubuntu-current`.

The source implementation was pushed from the Mac and both Ubuntu checkouts
fast-forwarded cleanly to `ce74e368f2aa40f9db90d1bb049f96fd533c5753` before
the live request. Candidate activation verified the pre-existing external
ancestors without changing them, then created only the root and `inbox`,
`outbox`, `events`, and `acks` at owner-owned `0700`. The local SlideStudio
checkout ignores `/tmp/mailbox-/` through its untracked `.git/info/exclude`.
P165 later added the fifth `diagnostics` child to every configured root.

`make test-p158-slidestud-mailbox` used the native `mailboxclient` to publish
one harmless `run` request with neither `environment` nor `execution_target`.
It resolved from the inbox default to `sandbox-dev` / `remote/sandbox-host`,
verified complete untruncated output from `ubuntu` on
`oracle-gustaw-janecki-ubuntu-flex-02` (`aarch64`), read the retained event
prefix through its final cursor, and wrote the exact ACK. The follow-up sandbox
P128 status reported zero active sessions, running commands, unreleased slots,
and unfinished jobs.

This is queued mailbox evidence. P158 did not use the direct mTLS endpoint as
a substitute for the mailbox result, and it did not onboard a new host.

## What P159 proved

P159 retained the owner-owned `0700` SlideStudio mailbox tree and added direct
workspace-file compatibility for selected-user-owned exact-`0644` request and
ACK pairs. Native `mailboxclient` publication remains exact `0600`, and
Runner-produced outbox and event files remain exact `0600`.

After the Mac source revision
`ee140ec5e4eefcdd5716a356bbbfec0f62c014b7` was pushed and both clean Ubuntu
checkouts fast-forwarded to it, the installed Mac gate published direct `0644`
request and ACK pairs without using the native publisher. It omitted the
selection pair, ran `uname -a` through the `slidestud-io` default, verified the
complete untruncated response and retained event prefix, and confirmed private
outbox/event projection modes.

The same flow was then run through ordinary workspace-created files at the
SlideStudio path. It returned `Linux` output from
`oracle-gustaw-janecki-ubuntu-flex-02` (`aarch64`), resolved to
`sandbox-dev` / `remote/sandbox-host`, consumed the exact direct ACK, and left
no request or ACK pair. The final sandbox P128 check reported zero active
sessions, running commands, unreleased slots, and unfinished jobs.

This is queued mailbox evidence. It does not replace a direct mTLS test and it
does not add or accept another remote host.

## What P166 proved

P166 installed the already handed-off P165 source on the Mac and used one new,
harmless direct-workspace `0644` request/zero-byte-marker pair in the
`slidestud-io` root. The JSON deliberately used a scalar `execution_target`,
so it was schema-invalid. Runner produced exactly one private exact-`0600`
`diagnostics/<request_id>.json` record with `invalid_request_schema`, consumed
the matching input pair, and kept the Mac service ready.

Read-only Mac authority checks found no mailbox exchange, local intent, job,
or command for the test identity. The queued Router dispatch-attempt count
remained unchanged at 49. No remote command, Gitea API request, Logger
workflow, bridge action, or normal outbox/event response was created. The real
Logger request identities were not replayed.

P166 is Mac mailbox-ingress acceptance only. It does not pass a new Ubuntu
host, queued bridge, direct mTLS endpoint, or physical power-loss gate.

## What is not accepted

| Item | Status | Reason |
| --- | --- | --- |
| Any other new remote profile | **NOT RUN** | A configured name or successful current host does not transfer host acceptance. |
| Physical power-loss durability | Unverified | P143 approved the software-process-crash-only path. A coordinated physical power-cut test is still required for this claim. |

## Required per-host P157 gate

For every new profile, P157 must record all of the following for that exact
machine:

1. clean source fast-forward from the Mac commit and matching commit proof;
2. owner-only host configuration, selected `ubuntu` account, service state,
   and private socket;
3. host-key pin and restricted bridge or direct mTLS materials, without
   exposing private keys;
4. expected listener/identity and profile-specific route health; and
5. a safe end-to-end request that selects the exact new target profile and
   proves the account/result.

If a named step is unavailable or has not been attempted, the host is `NOT
RUN`. If a required step runs and fails, record it as `FAIL` and stop with its
evidence. See the [setup P157 runbook](setup.md#7-onboard-each-additional-remote-host-p157)
and the [operations guide](operations.md) for current checks.
