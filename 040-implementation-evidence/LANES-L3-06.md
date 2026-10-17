# LANES-L3-06 — protected peers and constrained anchor endpoints

**State:** BLOCKED — source S and registered H/N source gates are delivered and
verified. Ordered predecessor L3-05 remains BLOCKED by the earlier L3
acceptance chain; this unit is not PASS and has no acceptance or evidence
closeout E. No endpoint was installed or attached to a production service.

## Pre-unit record

- Pre-unit Mac `dev` HEAD: `0272abdf507bb1c94777908aa984dccf96bf53e8`.
- Source predecessor: L3-05 source S `e526070b424e37cc8aa8e4a31c9bfca60e564424`.
- L3-05 evidence at the pre-unit HEAD: blob
  `e808d85104889fc36e577f8d29be6cb8915b2ecf`, path
  `040-implementation-evidence/LANES-L3-05.md`. L3-05 remains BLOCKED because
  L3-04/L3-03/L3-02/L3-01 and L2-09 native acceptance are still blocked by the
  unprovisioned L2-07 Linux and L2-08 Mac fixtures. No predecessor E is
  asserted.
- `LANES-L1-02` was already delivered PASS at source S
  `fbb866789a01f7dcc442164c51ccf255b95405db`; its evidence is preserved and
  unchanged.
- The owner authorized successive independent source-only units. L3-06 needs
  no production credential, publisher pause, maintenance window, mailbox
  transaction, installed helper, or service change. Synthetic policies cover
  the positive service-identity cases; native tests inspect only temporary
  Unix sockets and the current test process.
- B021/B024 remain BLOCKED with their retained evidence and held ACKs. BUG-022
  and BUG-023 remain deferred. The maintenance package remains its existing
  snapshot and was not rebuilt.

## Pre-unit read inventory

Blob IDs are from pre-unit HEAD `0272abdf507bb1c94777908aa984dccf96bf53e8`.

| Source | Blob |
| --- | --- |
| `AGENTS.md` | `3402877131d9fcea513f5bae756582e4127ee892` |
| Lane detailed design | `f7404c2d5a0b35706f3a9e867991bffcb6171d93` |
| Lane phased plan | `082e73ac62a7815c3ed15778eb8162d81a43c347` |
| Lane execution handbook | `d04a659df5c5834725ea05b9b047f4ad296b21bc` |
| Lane execution ledger | `c64517c4c56aaf09f67ac43794d6825db34be99e` |
| L3-05 predecessor evidence | `e808d85104889fc36e577f8d29be6cb8915b2ecf` |
| Test coverage manifest | `7edfca082e644a106680b1fa8021e05605e86a33` |
| `src/internal/release/security_anchor.go` | `e41b0fb24e739ad0eecee7470d2c9b9cd5fbd698` |
| `src/internal/release/l3_trust.go` | `e463407256c6ea710dd663a33d8021dea6a5e80b` |
| `src/internal/lanetests/manifest_test.go` | `8bc5e0389d63bc17e27f41c7469d71051cfbf3e4` |
| `Makefile` | `39cad4746bc66d27cafd249a9d7d3885f411dccc` |
| `go.mod` | `3073f69b23c7f8c851bda3d4ac299a5f81108e34` |

## Source deliverable

`src/internal/release/security_endpoint.go` defines three disjoint endpoints:
the Runner's `observe` endpoint, the Mac authority-agent-only read endpoint,
and the host release-transport endpoint. Each has a closed operation matrix
and strict, size-bounded JSON framing. Observe requests must name the installed
canonical host, a supported lane, and a digest-shaped resolved configuration
identity. The authority view returns detached public key, ledger, time, and
high-water state only. The transport endpoint accepts only time-probe,
addressed-record/cache, fixed outbox lookup, capacity-attestation, and PKI
mandate-reference operations. It accepts no shell, arbitrary path, measured
value, caller CSR, SAN, or key bytes. Endpoint sockets use fixed paths and are
served from a listener supplied by launchd/systemd; this change does not create
or unlink a host socket.

On macOS, `security_endpoint_darwin.go` reads the peer `LOCAL_PEERTOKEN`, checks
its PID against the active named launchd job, and uses Security.framework to
validate the audit-token process against the exact code identifier, Apple code
signature anchor, and configured Team ID requirement. A root UID alone is not
accepted for either root LaunchDaemon endpoint. The ordinary observe endpoint
uses kernel `LOCAL_PEERCRED` and `LOCAL_PEERPID` and still requires the
configured non-root Runner UID. A build with cgo disabled fails closed for the
privileged Mac endpoints; the UID-only observe check remains available.

On Linux, `security_endpoint_linux.go` obtains `SO_PEERCRED` and pins the peer
with `SO_PEERPIDFD`. Release-transport authorization requires the unique
`runner-release-transport` non-login passwd account, the exact
`runner-release-transport.service` MainPID and cgroup-v2 path, and the fixed
installed executable inode. The executable and every parent directory must be
root-owned, non-symlinked, and not group/other writable; the helper checks the
process executable path, inode, mode, and measured SHA-256. `systemctl` is
invoked by its fixed `/usr/bin/systemctl` path. Root and the shared `ubuntu`
account cannot impersonate the dedicated transport identity.

Eight H assertions cover positive Linux/Mac identity-policy tuples and
rejections for root, shared UID, wrong unit/cgroup/label/code, wrong executable
inode/mode, noncanonical host, open request fields, caller-selected paths or
PKI bytes, operation crossover, detached public authority state, and unsupported
endpoint paths. The native N assertion captures real local socket credentials
on the host, verifies the current non-root Runner UID where applicable, and
rejects the current process as a release-transport peer. It does not claim that
an installed dedicated transport unit exists or that a live service accepted
an endpoint.

## Mac source gates

Execution machine: `mac-control-plane` (Darwin arm64), Makefile-pinned Go
1.27.1; shared Mac Go caches reused.

| Command | Result |
| --- | --- |
| `make test-lanes UNIT=LANES-L3-06 LANES_TIER=H` | PASS — 8 registered H assertions |
| `LANES_NATIVE_HOST_ID=mac-control-plane make test-lanes UNIT=LANES-L3-06 LANES_TIER=N` | PASS — 1 registered native N assertion; `LOCAL_PEERTOKEN`, UID peer credentials, Security.framework mismatch refusal |
| `GOCACHE=/private/tmp/remote-session-runner-gocache GOMODCACHE=/private/tmp/remote-session-runner-gomodcache GOTOOLCHAIN=local '/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/toolchains/go1.27.1/bin/go' test -race ./src/internal/release -run '^TestLanesL306' -count=1` | PASS |
| `make check build` | PASS — full test suite, vet, smoke, and command builds |
| `python3 -m json.tool 040-implementation-evidence/LANES-test-coverage.json` and `git diff --check` | PASS |

The full suite emitted existing nonfatal duplicate `-lproc` linker warnings.

## Source delivery and synchronized host validation

Source revision S is
`8d1b7c876b00274f0284f15f27ebee8939a8d5a3`, committed on Mac `dev` as
`phase(LANES-L3-06): constrain anchor peer endpoints`. The explicit-key push to
primary Gitea succeeded. Direct `ls-remote` queries verified both `origin/dev`
and automatic mirror `github/dev` at exact S.

Delivery commands:

```sh
# Mac, primary Gitea push
git -c core.sshCommand='ssh -i /Users/tomasz.walczuk/.ssh/local-gitea-key -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' push origin dev

# Mac, automatic mirror verification
git -c core.sshCommand='ssh -i /Users/tomasz.walczuk/.ssh/gavrilo123456-github -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' ls-remote github refs/heads/dev

# Ubuntu, after clean dev/pre-pull verification
git -c core.sshCommand='ssh -i /home/ubuntu/.ssh/gavrilo123456-github -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' pull --ff-only origin dev
```

Before pulling, each Ubuntu checkout was clean on `dev` at
`0272abdf507bb1c94777908aa984dccf96bf53e8`. Each used the explicit GitHub
identity to `pull --ff-only origin dev`; each HEAD was verified at S before
testing. No tracked source was edited or copied directly on Ubuntu.

| Machine | SSH alias / Mac identity | H | N | `make check build` | Focused race | Final checkout |
| --- | --- | --- | --- | --- | --- | --- |
| `mac-control-plane` | local Mac checkout | PASS (8) | PASS (1) | PASS | PASS | `8d1b7c876b00274f0284f15f27ebee8939a8d5a3` |
| `linux-host` | `remote-session-runner` / `/Users/tomasz.walczuk/.ssh/remote-session-runner` | PASS (8) | PASS (1) | PASS | PASS | `8d1b7c876b00274f0284f15f27ebee8939a8d5a3` |
| `sandbox-host` | `sandbox.env` / `/Users/tomasz.walczuk/.ssh/dev.slidestud.io` | PASS (8) | PASS (1) | PASS | PASS | `8d1b7c876b00274f0284f15f27ebee8939a8d5a3` |
| `factory-sandbox` | `factory-sandbox` / `/Users/tomasz.walczuk/.ssh/factory-sandbox` | PASS (8) | PASS (1) | PASS | NOT RUN — `CGO_ENABLED=0`, no `cc` | `8d1b7c876b00274f0284f15f27ebee8939a8d5a3` |

Each Ubuntu host ran:

```sh
make test-lanes UNIT=LANES-L3-06 LANES_TIER=H
LANES_NATIVE_HOST_ID=<canonical-host-id> make test-lanes UNIT=LANES-L3-06 LANES_TIER=N
make check build
```

Linux-host and sandbox-host also ran the focused race command using
`/home/ubuntu/.local/share/remote-session-runner/toolchains/go1.27.1/bin/go`:

```sh
/home/ubuntu/.local/share/remote-session-runner/toolchains/go1.27.1/bin/go test -race ./src/internal/release -run '^TestLanesL306' -count=1
```

The test runs used a temporary `/tmp/lanes-l306.*` log directory with shell
trap cleanup after all commands completed. Generated build/test files stayed
outside the Git checkout; the Ubuntu Go caches were reused.
Factory-sandbox reported `CGO_ENABLED=0` and `cc=absent`, so the race gate was
NOT RUN. Its registered N gate and full source checks passed. Final checkout
verification showed all three hosts clean on `dev` at exact S.

## Scope and acceptance state

This was source implementation and isolated validation only. There were no
production credentials, mailbox requests or ACKs, installed socket/service
changes, production configuration changes, or production work changes. L3-06
source S is available to the next independent source unit. The unit remains
BLOCKED by its ordered L3-05/L3-04/L3-03/L3-02/L3-01/L2-09 acceptance chain; no
unit PASS, live acceptance, endpoint installation, or evidence closeout E is
claimed.
