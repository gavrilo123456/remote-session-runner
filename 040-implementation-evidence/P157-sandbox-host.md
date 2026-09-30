# P157 — `sandbox-host` onboarding

**Status:** IN PROGRESS. This is the separate physical-host acceptance gate for
the user-supplied bootstrap alias `sandbox.env`. It does not alter the
accepted status of `linux-host`.

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
`SHA256:n2i2F1TzaiIYMn4qM0VztKsKqHnh/kE5ndH/dn2znxk`. P157 will use a
dedicated copy of that public host entry only after a strict, non-interactive
match; it will never use `accept-new`, `ssh-keyscan`, or a Runner route that
consults `~/.ssh/config`.

The pin's current provenance is the user's pre-existing owner-only
`known_hosts` entry, rather than a newly accepted key. An independent
provider-console fingerprint record has not been supplied; P157 records that
limit without treating it as direct-API, bridge, or mailbox acceptance.

The host has not yet passed a P157 service, bridge, direct mTLS, selected-route
or mailbox request gate. Until those named gates pass, `sandbox-host` remains
**NOT RUN**.

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

The public `:8443` reachability, cloud firewall rule, certificate SAN, Runner
listener, direct mTLS application health, restricted bridge, Mac route health,
and mailbox request are still untested. The low free-space margin is monitored
through the candidate build; only P157-owned staged downloads and installer
caches may be removed.

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

The live `make test-p157-sandbox-host` gate has deliberately not run: the
candidate is not yet configured, deployed, or routed. Running it earlier would
only test the absence of the planned profile rather than a deployed P157 host.
