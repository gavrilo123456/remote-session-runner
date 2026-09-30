# P157 — `sandbox-host` onboarding

**Status:** PASS on 2026-09-30 after authorized remediation. The first direct
mTLS application attempt failed and is retained below; the repeated required
gates passed. This is the separate physical-host acceptance gate for the
user-supplied bootstrap alias `sandbox.env`. It does not alter the accepted
status of `linux-host`.

## Identity and scope

| Item | Selected value |
| --- | --- |
| Bootstrap SSH alias | `sandbox.env` |
| Bootstrap target | `ubuntu@132.226.205.205:22` |
| Runner remote target profile | `sandbox-host` |
| Runner environment | `sandbox-dev` |
| Runner execution context | `ubuntu-sandbox` |
| Planned direct endpoint name | `sandbox-poc` |
| Planned public direct endpoint | `https://132.226.205.205:8443` |

The bootstrap identity at `~/.ssh/dev.slidestud.io` is only for selected-host
administration. It is not a Runner dispatcher identity. P157 will create a
different owner-only dispatcher key and a separate owner-only `known_hosts`
pin under the Mac Runner service root. No private key is recorded in this
file or committed to Git.

## Pre-phase state

- **Mac checkout / account:**
  `/Users/tomasz.walczuk/projects/remote-session-runner` as
  `tomasz.walczuk`.
- **Mac branch / HEAD:** clean `dev...origin/dev` at
  `42793bb6fb3dccaf3a80f83c13fd626781aea4b6`
  (`docs(P156): close multi-inbox documentation evidence`).
- **Previous completed phase:** P156 is PASS. Its current-host documentation
  identifies every additional physical host as `NOT RUN` until its own P157
  evidence passes.
- **Durability boundary:** P143's accepted result is software-process-crash
  recovery only. Physical power-loss durability remains unverified and is not
  exercised by P157.

## Fresh-context revisions and file inventory

The following authoritative inputs were read before P157 implementation work:

| File | Git blob revision |
| --- | --- |
| `020-initial-design/010-remote-session-runner-initial-design.md` | `12440598a0dba018313196cd80c7848284868f28` |
| `030-detailed-design/010-remote-session-runner-detailed-design.md` | `b24f095d62e6000abed690abf125fd60be4eff1c` |
| `030-detailed-phased-implementaion-plan/010-remote-session-runner-detailed-phased-implementaion-plan.md` | `3f7de2b5ed99332636f15b9d947be5d5662e5f53` |
| `040-improvments/010-multiple-inboxes-and-remote-machines/005-initial-idea.md` | `1fc6e1f95c85183b31b84c2814320d3a4ce82e6b` |
| `040-improvments/010-multiple-inboxes-and-remote-machines/010-multiple-inboxes-and-remote-machines-detailed-design.md` | `23b276fc18c7a963b74b8d10de064580988be4df` |
| `040-improvments/010-multiple-inboxes-and-remote-machines/015-multiple-inboxes-and-remote-machines-phased-implementation-plan.md` | `632b29756f9f1281c169eefd6faaf67338b044f9` |
| `040-implementation-evidence/000-preimplementation-decisions.md` | `7c9a9dde397e8b0fee0cd0b21109dfafbd46ccec` |
| `040-implementation-evidence/P156.md` | `866ce7a8b94e2ba26f86341052ad808b6e7dd7b2` |

The review also covered `AGENTS.md`, `README.md`, the active P155/P156
evidence, the V2 configuration validator and mailbox selection code,
`sshclient`, remote-route/health code, `mailboxclient`, the existing P155
Mac host gate, the Makefile, Linux service and queued-bridge installers, and
the P157 setup/current-host documentation.

## Initial trust and host facts

The user supplied this bootstrap target:

```sshconfig
Host sandbox.env
  HostName 132.226.205.205
  IdentityFile ~/.ssh/dev.slidestud.io
  User ubuntu
```

The pre-existing owner-only user SSH known-hosts file contains an ED25519
entry for `132.226.205.205` with fingerprint
`SHA256:n2i2F1TzaiIYMn4qM0VztKsKqHnh/kE5ndH/dn2znxk`. P157 used a dedicated
copy of that public host entry only after a strict, non-interactive match; it
never used `accept-new`, `ssh-keyscan`, or a Runner route that consults
`~/.ssh/config`.

The pin's current provenance is the user's pre-existing owner-only
`known_hosts` entry, rather than a newly accepted key. An independent
provider-console fingerprint record has not been supplied; P157 records that
limit without treating it as direct-API, bridge, or mailbox acceptance.

At this preflight point, the host had not yet passed a P157 service, bridge,
direct mTLS, selected-route, or mailbox request gate. It was therefore
**NOT RUN** at that point.

## Strict bootstrap preflight

**Result:** PASS for the pinned SSH transport and host prerequisites below;
this is not a Runner application or P157 acceptance result.

From the Mac, a non-interactive read-only SSH command used `-F /dev/null`,
`IdentitiesOnly=yes`, `IdentityAgent=none`, `StrictHostKeyChecking=yes`,
`UpdateHostKeys=no`, the existing owner-only user `known_hosts` file, and the
bootstrap identity. The command did not accept or scan a host key and did not
print private key material.

| Check | Result |
| --- | --- |
| Strict host-key match | PASS — existing ED25519 pin matched `SHA256:n2i2F1TzaiIYMn4qM0VztKsKqHnh/kE5ndH/dn2znxk` |
| Account / UID | `ubuntu` / `1001` |
| Hostname | `oracle-gustaw-janecki-ubuntu-flex-02` |
| OS / architecture | Ubuntu 22.04 / `aarch64` |
| Noninteractive `sudo` | PASS |
| Candidate checkout | present, clean `dev...origin/dev` at `a9ac6fcce89732e48b94387585c2651912167a37` |
| Selected Go 1.27.1 | absent; provision native `linux/arm64` before service build |
| `runnerd.service` | inactive; no P157 service has been installed |
| Candidate concrete bind address | `10.0.0.14` on `enp0s3` |
| Root filesystem | 52G total, 50G used, 2.1G free (96% used) |

At this point, public `:8443` reachability, cloud firewall rule, certificate
SAN, Runner listener, direct mTLS application health, restricted bridge, Mac
route health, and mailbox request remained untested. The low free-space margin
was monitored through the candidate build; only P157-owned staged downloads
and installer caches could be removed.

## Planned implementation and gates

1. Add a fail-closed Linux architecture mapping that accepts only `x86_64`/`amd64`
   and `aarch64`/`arm64`, so the pinned Go 1.27.1 service and bridge installers
   can validate the correct Linux binary without weakening their account,
   source-commit, ownership, or toolchain checks.
2. Add an opt-in P157 Mac acceptance test that uses the native
   `mailboxclient` API, requires an explicit configured sandbox mailbox route,
   checks profile-specific router health, publishes a harmless remote command,
   verifies `ubuntu` and the expected hostname/architecture, reads events, and
   writes the exact ACK.
3. Update the P157 runbook/template wording so a full reviewed Mac V2
   candidate can be constructed before the controlled acceptance request while
   preserving `default` and `analytics`.
4. Run focused tests, `make test`, `make vet`, and `git diff --check` on the
   Mac. Commit, push with the dedicated GitHub key, and fast-forward both the
   existing Ubuntu checkout and this candidate checkout before installing or
   testing Runner services on the candidate.
5. Use independent candidate-only state, server leaf key/certificate, direct
   client leaf key/certificate, client CA copy, bridge authorization, and
   dispatcher key. Prove direct mTLS application health separately from the
   queued mailbox result. Finish with a zero-active-work status check.

Any required gate that is actually run and fails is recorded as **FAIL** and
stops P157. A host that has not reached a gate remains **NOT RUN**.

## Mac preparation results

| Gate | Result |
| --- | --- |
| Focused P157 test without live opt-in | PASS — compiled and skipped with its required gate variable absent |
| `sh -n deploy/linux/install-systemd-service.sh` | PASS |
| `sh -n deploy/ssh/install-queued-bridge.sh` | PASS |
| `make test` | PASS |
| `make vet` | PASS |
| `git diff --check` | PASS |

At this preparation point, the live `make test-p157-sandbox-host` gate had not
run: the candidate was not yet configured, deployed, or routed. Running it
then would only have tested the absence of the planned profile rather than a
deployed P157 host.

## First candidate deployment attempt and failed direct mTLS gate

The P157 preparation revision was committed on the Mac as
`2693a8091c926e66239243ad89fa1fa6648ebbbb`
(`phase(P157): prepare sandbox host onboarding`), pushed to `origin/dev` with
the dedicated Mac GitHub identity, then fast-forwarded from clean `dev`
checkouts on both the existing Ubuntu host and the candidate. Each checkout,
`origin/dev`, and the Mac checkout resolved to that exact revision before any
candidate service installation.

The candidate received independent owner-only runtime configuration, SQLite
state path, mTLS materials, and an ARM64 Go 1.27.1 toolchain. Its service
started as `ubuntu`, created its private socket, and listened on
`10.0.0.14:8443`. These are service-install observations only; the bridge,
Mac candidate configuration, router health, and mailbox request were not
enabled or run.

| Required P157 gate | Result |
| --- | --- |
| Candidate `runnerd.service` / private socket / `10.0.0.14:8443` listener | PASS |
| Public direct mTLS Runner health | **FAIL** |
| Restricted queued bridge | NOT RUN |
| Activated Mac `sandbox-host` route and profile health | NOT RUN |
| `make test-p157-sandbox-host` native mailbox request | NOT RUN |
| Final zero-active-work status | NOT RUN |

From the Mac, the owner-only sandbox CA, client certificate, and client key
were supplied to the direct health request for
`https://132.226.205.205:8443/health/ready`. It returned:

```text
curl: (56) LibreSSL SSL_read: LibreSSL/3.3.6: error:1404C418:SSL routines:ST_OK:tlsv1 alert unknown ca, errno 0
HTTP 000
```

The candidate service journal reported that the client certificate was signed
by an unknown authority because its signature was considered insecure:

```text
tls: failed to verify certificate: x509: certificate signed by unknown authority
(possibly because of "x509: cannot verify signature: insecure algorithm ECDSA-SHA1"
while trying to verify candidate authority certificate "Remote Session Runner PoC CA")
```

Public certificate metadata confirmed the newly issued candidate client and
server leaves were signed with `ecdsa-with-SHA1`, while the already accepted
current-host client leaf uses `ecdsa-with-SHA256`. The cause is the Mac
LibreSSL default signing selection during first issuance. No private key
material is recorded here.

The first installer run also left its own
`tmp/install-go-cache.*` directory after the service transition. Go had made
the downloaded module directories read-only, so plain `rm -rf` could not clean
that task-owned cache. The P157 remediation changes both Linux installers to
restore owner write permission without following symbolic links and remove only
their own cache. The service installer preserves the active service's immediate
zero-active-work-to-restart boundary; the bridge removes its cache after its
build. The retry verified that no such cache remained.

The user authorized the remediation after this recorded failure. It issued new
candidate-only SHA-256 leaves, repaired the task-owned cache cleanup, and
repeated the required gates from the direct mTLS check. This prior failure is
not treated as a pass or hidden by the retry.

### Pre-activation mailbox-test guard

Before the reviewed Mac candidate configuration contained `ubuntu-sandbox`,
the opt-in `make test-p157-sandbox-host` command was invoked twice during
remediation source validation. Both invocations failed at the test's local
allow-list check: the installed `analytics` mailbox only allowed `mac-local`
and `ubuntu-current`. The test performs that check before opening a native
mailbox client, so neither invocation published a request or ran a command on
either Ubuntu host. This is a configuration prerequisite observation, not a
P157 mailbox acceptance result. The controlled mailbox gate remains NOT RUN
until the reviewed candidate configuration is activated.

## Remediation and final acceptance

The remediation source revision is
`8873852ddc9ab33093c105371de93a3695d99b89`
(`fix(P157): clean installer Go caches safely`). It was committed and pushed
from the clean Mac `dev` checkout with the dedicated GitHub identity. Both
Ubuntu checkouts then fast-forwarded cleanly with their specified GitHub
identity. Immediately before final host checks, Mac `HEAD`, Mac `origin/dev`,
the existing Ubuntu checkout, and the sandbox checkout all resolved to that
exact revision.

### Certificate and installer repair

The replacement certificate operation used Mac OpenSSL 3.6.3 with explicit
SHA-256 signing. The sandbox server kept its existing host-only RSA private
key; only a public CSR crossed to the Mac signer. A fresh sandbox-only direct
client key was generated on the Mac. The replacement leaves were verified
against the existing PoC CA, matched their expected public keys, and had these
public constraints:

| Leaf | Required public constraints | Result |
| --- | --- | --- |
| Server | SHA-256 signature; `serverAuth`; `IP:132.226.205.205` SAN | PASS |
| Direct client | SHA-256 signature; `clientAuth`; URI SAN `urn:remote-session-runner:controller:runner-tomasz-sandbox-direct` | PASS |

The server public certificate was staged owner-only, backed up only for the
controlled restart, and restored automatically if the installer failed. It did
not fail. The Linux installer repeated its read-only status gate before the
active restart, then the repaired cache cleanup removed its private
`install-go-cache.*` tree. The bridge installer likewise removed its private
`queued-bridge-go-cache.*` tree after its build. No shared Go cache was removed.

The strict-bootstrap host-key pin remains a copied public ED25519 record from
the user's existing owner-only known-hosts file. Its provenance limitation
remains: no independent provider-console fingerprint record was supplied.

### Required gate results after remediation

| Gate | Evidence | Result |
| --- | --- | --- |
| Candidate service identity | `ubuntu`, `oracle-gustaw-janecki-ubuntu-flex-02`, `aarch64`; active `runnerd.service`, private socket, and listener `10.0.0.14:8443` | PASS |
| TLS transport | OpenSSL 3 `s_client`: TLS 1.3, verified peer, SHA-256 handshake signature, and client certificate accepted | PASS |
| Runner direct application | `curl` to `https://132.226.205.205:8443/health/ready` returned Runner readiness JSON with `direct_mtls: ready` and `HTTP 200` | PASS |
| Restricted bridge | `queued_bridge_status=ready`, dedicated dispatcher fingerprint `SHA256:lb3XAMkTkVWaGEp/3XckUM/gMTKwXUuQoyXKz8WWuNw`, source revision `8873852…` | PASS |
| Mac candidate activation | Owner-only `mac.next.yaml` validated, the installer quiesced and restarted both LaunchAgents, and active `mac.yaml` retained `default` plus `analytics` | PASS |
| Mac profile health | `remote_router/linux-host=ready` and `remote_router/sandbox-host=ready`, zero mailbox backlog | PASS |
| Native end-to-end request | `make test-p157-sandbox-host` passed; its native `mailboxclient` request used `analytics`, `sandbox-dev`, and `remote/sandbox-host`, verified exact output `P157_SANDBOX_OK`, `ubuntu`, `oracle-gustaw-janecki-ubuntu-flex-02`, `aarch64`, read events, and ACKed the response | PASS |
| Final host status | P128 read-only counts: `active_sessions=0`, `running_commands=0`, `unreleased_slots=0`, `unfinished_jobs=0` | PASS |
| Task-owned cleanup | No `install-go-cache.*` or `queued-bridge-go-cache.*` remained; rotation/bridge scripts, logs, staged public files, certificate scratch, and superseded candidate backup were removed | PASS |

The active Mac policy leaves `default` limited to `mac-local` and
`ubuntu-current`. It adds `ubuntu-sandbox` only to the `analytics` allow-list;
the new profile is not an automatic target or a fallback. The direct endpoint
is `sandbox-poc` at `https://132.226.205.205:8443` and the queued context is
`sandbox-dev` / `remote/sandbox-host`.

P157 proves this named host's service, direct mTLS application path, restricted
queued route, and one controlled mailbox request. It does not change P143's
software-process-crash-only durability boundary: physical power-loss recovery
remains unverified pending a coordinated physical power-cut test.

### Documentation closeout validation

The post-acceptance documentation records both accepted public endpoints in the
OpenAPI inventory. `TestP003OpenAPIRoutesAndSecurity` now requires exactly the
`linux-host` and `sandbox-host` URLs, so a later documentation edit cannot
silently drop either endpoint. On the Mac, that focused test, `make test`,
`make vet`, `git diff --check`, and OpenAPI JSON parsing all passed before this
evidence closeout was committed.
