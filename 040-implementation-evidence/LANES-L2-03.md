# LANES-L2-03 — Direct and management endpoint/TLS material validation

**State:** PASS — source acceptance only. This unit validates fixed endpoint
maps and test-only certificate material. It did not contact a public endpoint,
install a certificate, change a firewall/NAT rule, or start a service.

## Predecessor and scope

- Ordered predecessor: L2-02 evidence closeout E
  `47cd2613add7c6a5ea327c40e86ac862cff42854`, clean on Mac `dev`, Gitea,
  GitHub, and all three Ubuntu `dev` checkouts.
- Source S: `94cca54f375260a646d8fe27d6f1311ec3cccbd6`.
- L2-03 implements the fixed direct endpoint table for all nine lane/profile
  combinations and the three fixed Ubuntu release-transport `:9443` routes.
  Direct route values derive from the closed Linux namespace registry; PRD is
  represented for map compatibility but certificate validation/client
  construction refuse PRD. The management table has only the three Ubuntu
  public endpoints; the Mac remains on its protected local socket.
- Direct material validation checks certificate/key matches, validity and
  usage, server IP and URI SANs, exact client URI SAN, certificate and SPKI
  fingerprints, and that the verified chain terminates at the configured CA
  fingerprint. The direct principal map must contain exactly the matching
  lane/profile URI, `direct_mtls` type, and controller ID. A server IP SAN may
  accompany optional DNS SANs; the fixed-IP client still verifies the literal
  public IP. Client certificates have only the exact required URI SAN.
- The Dev/QAS set validator requires all six lane/profile bundles, rejects
  cross-lane certificate, CA, public-key, and principal reuse, and checks
  distinct server and client leaves across each lane's three profiles. A
  separate check keeps the direct-control material disjoint from the
  management `:9443` trust domain.
- `BuildDirectTLSClientConfig` and
  `BuildReleaseTransportTLSClientConfig` produce TLS 1.3 configs only for
  canonical routes, verify the literal IP and pin the server URI/certificate/
  SPKI. They load no files and open no socket.
- Every identity was generated in memory for tests. The N assertion opened
  only an ephemeral loopback listener; it proved a valid handshake and
  rejection of a foreign-lane client, wrong server URI, wrong literal-IP SAN,
  and wrong client URI. No production key, credential, service, mailbox,
  Runner request, or ACK was used or changed. No internet/public reachability,
  release transport startup, certificate issuance, firewall, or NAT proof is
  claimed.
- `LANES-B021-02` and `LANES-B024-02` remain **BLOCKED** with their existing
  evidence preserved. BUG-022/023 remain deferred. This unit published no
  mailbox requests or ACKs and made no change to the standing publication
  pause.

## Pre-unit inventory at E `47cd261`

The fixed route, SAN, principal, and trust requirements; L2-03 handbook and
coverage contract; L2-02 evidence; lane namespace/profile validation; existing
direct HTTPS server/client TLS behavior; principal-map parser; and coverage
dispatcher were reviewed before implementation.

| Read input | Blob at predecessor E |
| --- | --- |
| `AGENTS.md` | `3402877131d9fcea513f5bae756582e4127ee892` |
| Detailed design | `f7404c2d5a0b35706f3a9e867991bffcb6171d93` |
| Phased implementation plan | `082e73ac62a7815c3ed15778eb8162d81a43c347` |
| Execution handbook | `d04a659df5c5834725ea05b9b047f4ad296b21bc` |
| Execution ledger | `f090f72bbd3cd47cd3779ebf53226cfced07e29f` |
| L2-02 evidence | `cd193b80b8600afe03a78a4896bd584e8a98d6a9` |
| Lane coverage manifest | `7889a7e637b2a023952bc638c29a9bf8037b52a4` |
| Lane namespace derivation | `29618b35c8c2c17621334673e5a8a212169c92a9` |
| V3 lane profile validation | `c7b2b15f3ad7d4fe661b17676d2c9a5ea7b8af88` |
| V3 lane profile tests/fixtures | `de6a133889993fc718adda84d0fbd707b1fbdfce` |
| Existing direct HTTPS server | `67d1974ae04ec81473c8c38b8d2b6db9e4a474d1` |
| Existing direct HTTPS TLS tests/principal-map shape | `766c05d963800052924ffb342a6f6c295e5235db` |
| Existing Mac endpoint client | `3cc99331c4d4cbd48f6594d5d83ce5b84efab36b` |
| Coverage dispatcher | `f5cc9bec4d6046257e5fa7853bb72dc9a278fe1b` |
| Coverage dispatcher tests | `fa90af4fc97e49da9f47dec1666c38eddf69ed15` |

## Source implementation and registered proof

Added `src/internal/laneinstall/direct_tls.go` and generated-identity tests in
`src/internal/laneinstall/lanes_l203_test.go`. Registered four hermetic H
assertions and one local-network N assertion in
`040-implementation-evidence/LANES-test-coverage.json`; the manifest test now
asserts that L2-03 carries both required tiers.

H covers the nine direct endpoint tuples, the three fixed management routes,
valid direct and management material, fixed client configuration, wrong
identity/IP/URI/key/CA/fingerprint/expiry/controller-map rejection, full Dev/QAS
material coverage, lane and management trust separation, and protected PRD
client-construction refusal. N performs real TLS 1.3 mutual-TLS handshakes over
loopback only and proves the valid case and the four listed identity negatives.

The validator reports one safe generic error for malformed material; it never
returns or logs PEM, keys, or caller-provided paths. Certificate verification
requires the chain to terminate at the configured fingerprint even when the
trust bundle contains additional roots.

### Failed checks and corrections

1. An early manifest-loader test incorrectly expected 69 units. L2-03 already
   existed as a placeholder, so the total remains 68; the assertion was
   corrected and the manifest/dispatcher tests passed.
2. The Mac shell does not expose `gofmt` on `PATH`; formatting checks use the
   pinned Go 1.27.1 toolchain's sibling binary from the Makefile path.
3. Sandbox policy denied normal writes to `.git/index.lock`. Only the four
   reviewed L2-03 files were staged and committed with the authorized Git
   write; no other worktree content was staged.
4. The source push to Gitea succeeded, but Git could not update the local
   `refs/remotes/origin/dev` tracking lock. Direct `ls-remote` checks confirmed
   both Gitea and GitHub at S, and all host pulls advanced by fast-forward.

## Source S and validation

| Machine | H (4 assertions) | N (1 assertion) | `make check build` | Focused race | Format/JSON/diff | Final checkout |
| --- | --- | --- | --- | --- | --- | --- |
| Mac `mac-control-plane` | PASS | PASS | PASS | PASS | PASS | clean `dev`, S |
| `linux-host` (`oracle-yuta-konopka-ubuntu-micro-02`) | PASS | PASS | PASS | PASS | PASS | clean `dev`, S |
| `sandbox-host` (`oracle-gustaw-janecki-ubuntu-flex-02`) | PASS | PASS | PASS | PASS | PASS | clean `dev`, S |
| `factory-sandbox` (`oracle-waclaw-dziwulski-ubuntu-micro-01`) | PASS | PASS | PASS | NOT RUN: `CGO_ENABLED=0`, no `cc` | PASS | clean `dev`, S |

On all four machines, H and N used:

```sh
make test-lanes UNIT=LANES-L2-03 LANES_TIER=H
make test-lanes UNIT=LANES-L2-03 LANES_TIER=N
make check build
```

Mac and the two hosts with C toolchains also ran:

```sh
GOTOOLCHAIN=local go test -race ./src/internal/laneinstall ./src/internal/config ./src/internal/lanetests -run '^(TestLanesL203|TestLanesL101ManifestMatchesPlan)' -count=1
```

The pinned Go 1.27.1 toolchain ran all commands. Mac reused
`/private/tmp/remote-session-runner-gocache` and
`/private/tmp/remote-session-runner-gomodcache`; Ubuntu used the selected
account's normal caches. Format used `gofmt -d` on the three changed Go files,
JSON used `python3 -m json.tool`, and `git diff --check` passed. Factory's race
test is explicitly **NOT RUN** because its selected toolchain reports
`CGO_ENABLED=0` and no `cc` is installed.

The focused H/N assertions and entire `make check build` gate passed on every
machine at Source S. That target passed full `go test ./...`, `go vet ./...`,
the command smoke checks, and command builds. All Ubuntu validation followed
the GitHub fast-forward to exact S; no source file was edited or copied on an
Ubuntu host.

## Git delivery and closeout

On Mac `dev`, Source S was committed from the reviewed four-file change and
pushed to primary Gitea with
`/Users/tomasz.walczuk/.ssh/local-gitea-key`. Direct remote reads confirmed
Gitea `origin/dev` and automatic mirror `github/dev` both equal
`94cca54f375260a646d8fe27d6f1311ec3cccbd6`. The local tracking-ref lock error
did not affect the successful primary push.

Before each pull, all three Ubuntu checkouts were clean on `dev` at predecessor
E `47cd261...`. Each then used
`/home/ubuntu/.ssh/gavrilo123456-github` to pull `origin/dev --ff-only` and
reached exact S. SSH used the configured host aliases, explicit Mac identities,
`IdentitiesOnly=yes`, `BatchMode=yes`, `StrictHostKeyChecking=yes`,
`UpdateHostKeys=no`, and the previously verified host keys:

| Host | Mac alias / explicit identity | Final checkout |
| --- | --- | --- |
| `linux-host` | `remote-session-runner` / `/Users/tomasz.walczuk/.ssh/remote-session-runner` | clean `dev`, S |
| `sandbox-host` | `sandbox.env` / `/Users/tomasz.walczuk/.ssh/dev.slidestud.io` | clean `dev`, S |
| `factory-sandbox` | `factory-sandbox` / `/Users/tomasz.walczuk/.ssh/factory-sandbox` | clean `dev`, S |

This file and the L2-03 ledger PASS row form the docs-only evidence closeout E.
E is committed and delivered through the same Gitea, exact GitHub mirror, and
three clean Ubuntu fast-forward workflow. It gates `LANES-L2-04`.

## Acceptance boundary

L2-03 is **PASS** for source acceptance. Runtime certificate issuance,
management/listener installation, public-IP reachability, cloud security
rules/NAT, listener startup, installed TLS attestation, and post-start external
handshakes remain future operation-specific gates. No PRD material or service
was changed.
