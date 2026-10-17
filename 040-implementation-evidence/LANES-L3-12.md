# LANES-L3-12 — issuer and installer issuance chains

**State:** BLOCKED in the ordered lane sequence. Source S and the five H
assertions passed on the Mac and all three clean synchronized Ubuntu checkouts.
Required F acceptance is NOT RUN. The ordered predecessor L3-11 remains
BLOCKED because L3-10 privileged F is NOT RUN; this record does not claim
L3-11 PASS, L3-12 PASS, live acceptance, or an evidence closeout E.

## Pre-unit state and scope

- Pre-unit Mac `dev` HEAD was `83c80a6365c0f6a8f18658de33c38a61bdc45590`,
  the L3-11 evidence closeout. Its source S was
  `83c74829cdf95ffeca448b77b1e82ff6fc4d8d0f`. L3-11 H/N source gates passed,
  while its ordered L3-10 F predecessor remains NOT RUN.
- The active L1-02 request is already delivered from the L1-01 closeout at
  `b25468d`; its evidence and status were preserved. This continuation advances
  to the next unfinished unit in the recorded ordered sequence, L3-12.
- The owner's incremental-readiness authorization permits this independent
  source unit while L3-11's F and the earlier infrastructure chain remain
  blocked. BUG-021/B024 evidence remains BLOCKED and untouched. BUG-022/023
  baseline repairs remain DEFERRED.
- Source tests use fresh `t.TempDir()` state, generated fixture CA and signing
  keys, and fixture-only approved mandates. They do not use production
  credentials, production keys, publishers, mailboxes, installed services,
  storage, firewall rules, or host configuration.
- Required L3-12 F was not attempted: the measured issuer/installer services
  and their protected platform identities are not installed; L3-19 is the
  prescribed first continuous host-management bootstrap. No privileged service
  change or production credential use is part of this source continuation.

## Pre-unit input inventory

Blob IDs below are from pre-unit HEAD `83c80a6365c0f6a8f18658de33c38a61bdc45590`.

| Input | Blob |
| --- | --- |
| `AGENTS.md` | `3402877131d9fcea513f5bae756582e4127ee892` |
| Lane initial idea | `503e54457228da265666ae48ceb2e22a0c0b81f` |
| Lane detailed design | `f7404c2d5a0b35706f3a9e867991bffcb6171d93` |
| Lane phased plan | `082e73ac62a7815c3ed15778eb8162d81a43c347` |
| Lane execution handbook | `d04a659df5c5834725ea05b9b047f4ad296b21bc` |
| Execution ledger | `a5cfea695ccadf6a83ca12ee0c8f187ba36a00fe` |
| L3-11 predecessor evidence | `795d3344b52d59d1ecca1875c799762f3a05bc90` |
| Lane coverage manifest | `2bc091c46dedc9cde24eb08d64e9cfae21d82647` |
| `Makefile` | `39cad4746bc66d27cafd249a9d7d3885f411dccc` |
| `go.mod` | `3073f69b23c7f8c851bda3d4ac299a5f81108e34` |
| L2 PKI implementation | `a448a69580d7648260669fdce222679c9c2dd558` |
| L2 PKI tests | `891584963439e368c0b3e6f7fa90344fc6b0687f` |
| Authority journal | `9fa3a94664655c1cb07b02cffb4128d2ed2aa6e2` |
| Authority signer/approval tool | `368657abfe81f7cddc0178f0a934a7f9348a6891` |
| Lane test dispatcher | `f5cc9bec4d6046257e5fa7853bb72dc9a278fe1b` |
| Coverage manifest tests | `92e7048757e032b66229449887ebe6bbd7ae2459` |

## Source implementation

- The PKI issuer now accepts only a host-signed request. It resolves the opaque
  mandate ID from the issuer journal, verifies the stored mandate signature,
  then checks the exact host, role, URI/IP SAN, management or bootstrap tuple,
  CSR, public-key fingerprint, profile, validity, and host attestation before
  issuing.
- Issuer and approval capabilities are separated. The issuer journal interface
  can resolve mandates, commit issuance, and inspect bootstrap state; mandate
  registration and first-health recording are separate capabilities.
- The root authority journal now retains approved PKI mandates, the exact
  public request, non-secret certificate/trust package and signed receipt, and
  monotonic per-host bootstrap health gates in its existing full-sync,
  append-only SQLite state. Commit is atomic; a mandate cannot be consumed
  twice or have its retained package/receipt replaced. Invalid requests do not
  consume the mandate.
- The bootstrap issuance branch binds contract digest, bootstrap generation,
  installer artifact tuple, and pinned bootstrap host-attestation key. The
  journal refuses another bootstrap issuance for a host after issuance and
  permanently refuses new bootstrap mandates after its first health gate.
- The local leaf key remains in its host-local store with mode `0600`; the
  request, receipt, package, and journal record have no private-key field. The
  coverage contract now registers five L3-12 H assertions.

## Source gates and corrections

The Mac used pinned Go 1.27.1 and shared caches
`/private/tmp/remote-session-runner-gocache` and
`/private/tmp/remote-session-runner-gomodcache`.

| Machine | Required gates | Result |
| --- | --- | --- |
| `mac-control-plane` | `make test-lanes UNIT=LANES-L3-12 LANES_TIER=H`; `make test-lanes UNIT=LANES-L2-04 LANES_TIER=H`; `make check build`; `/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/toolchains/go1.27.1/bin/go test -race ./src/internal/laneinstall ./src/internal/release -run 'TestLanesL204|TestLanesL312' -count=1` | PASS — five L3-12 assertions; five PKI regression assertions; full Go tests, vet and command builds; focused race |
| `linux-host` | `make test-lanes UNIT=LANES-L3-12 LANES_TIER=H`; `make test-lanes UNIT=LANES-L2-04 LANES_TIER=H`; `make check build`; `/home/ubuntu/.local/share/remote-session-runner/toolchains/go1.27.1/bin/go test -race ./src/internal/laneinstall ./src/internal/release` | PASS |
| `sandbox-host` | `make test-lanes UNIT=LANES-L3-12 LANES_TIER=H`; `make test-lanes UNIT=LANES-L2-04 LANES_TIER=H`; `make check build`; `/home/ubuntu/.local/share/remote-session-runner/toolchains/go1.27.1/bin/go test -race ./src/internal/laneinstall ./src/internal/release` | PASS |
| `factory-sandbox` | `make test-lanes UNIT=LANES-L3-12 LANES_TIER=H`; `make test-lanes UNIT=LANES-L2-04 LANES_TIER=H`; `make check build` | PASS |
| `factory-sandbox` | Race instrumentation | NOT RUN — selected toolchain reports `CGO_ENABLED=0`; no `cc` is installed |
| `all three Ubuntu hosts` | Required F acceptance | NOT RUN — protected issuer/installer services are not installed; no service or credential change was made |

Two command-wrapper mistakes were corrected before host test results were
recorded. A preliminary bare `go version` failed because `go` is not on the
Ubuntu PATH; no test started. The Makefile's explicit
`/home/ubuntu/.local/share/remote-session-runner/toolchains/go1.27.1/bin/go`
then passed its own version check and ran the gates. A separate wrapper with a
nested quoted race-test pattern failed in the local shell before remote
validation; it was rerun without nested quotes. The successful results above
are from the corrected commands.

The DNS names in the predecessor evidence did not resolve from this Mac shell.
Read-only `ssh -G` confirmed the configured profiles, endpoints, and identities:
`remote-session-runner` → `129.151.232.40`, identity
`/Users/tomasz.walczuk/.ssh/remote-session-runner`;
`sandbox.env` → `132.226.205.205`, identity
`/Users/tomasz.walczuk/.ssh/dev.slidestud.io`; and `factory-sandbox` →
`145.241.255.66`, identity
`/Users/tomasz.walczuk/.ssh/factory-sandbox`. SSH used
`IdentitiesOnly=yes`, `BatchMode=yes`, `StrictHostKeyChecking=yes`, and
`UpdateHostKeys=no`. No host file was edited outside Git.

## Source S delivery and host validation

Source S is `1880112ba6ce2a007f4b79ada4b330738400d404`
(`phase(LANES-L3-12): persist exact PKI issuance state`). The Mac `dev` source
was pushed to primary Gitea `origin/dev`; direct `ls-remote` returned S. The
automatic GitHub `github/dev` mirror also returned the exact S. Before pulling,
all three Ubuntu checkouts were clean on `dev` at predecessor
`83c80a6365c0f6a8f18658de33c38a61bdc45590`. Each fast-forwarded from GitHub
with `/home/ubuntu/.ssh/gavrilo123456-github`, reached exact S, passed the
gates above, and remained clean:

| Required machine | SSH profile / verified endpoint | Final source state |
| --- | --- | --- |
| `linux-host` | `remote-session-runner` / `129.151.232.40` | clean `dev`, S; H/full/vet/build/race PASS |
| `sandbox-host` | `sandbox.env` / `132.226.205.205` | clean `dev`, S; H/full/vet/build/race PASS |
| `factory-sandbox` | `factory-sandbox` / `145.241.255.66` | clean `dev`, S; H/full/vet/build PASS; race unavailable as above |

No PKI issuer, installer, authority, Runner, or lane service was installed or
restarted. No credential or certificate was issued outside the isolated test
fixtures. B021/B024 remain BLOCKED and BUG-022/023 remain DEFERRED.
