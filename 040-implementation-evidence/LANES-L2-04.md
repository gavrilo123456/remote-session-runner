# LANES-L2-04 — PKI local-key, CSR, package, and issuance interfaces

**State:** PASS — source acceptance only. No production keys, credentials,
certificates, services, listeners, mailbox files, requests, ACKs, firewall rules,
or installed configuration were created or changed.

## Predecessor and scope

- Ordered predecessor: L2-03 evidence closeout E
  `ebaa5e747c14e838026b1931544cf3b288877594`.
- E was clean on Mac `dev`, primary Gitea, the exact GitHub mirror, and all
  three Ubuntu `dev` checkouts before L2-04 began.
- L2-04 source S: `dcbe658572763c7b870a725193a2e078ae1ca28a`.
- BUG-021/B024 evidence remains preserved and those units remain **BLOCKED**.
  BUG-022/023 remain deferred. This source unit did not wait on the B021-02
  maintenance closeout and did not change its retained work or publication
  pause.

## Implementation

Added `src/internal/laneinstall/pki.go` and fixture coverage in
`src/internal/laneinstall/lanes_l204_test.go`. Registered five H assertions
and one N assertion in `LANES-test-coverage.json`; the manifest test requires
both proof tiers.

- The fixed host map permits only the Mac authority client identity and the
  three Ubuntu release-transport server identities. It derives exact URI and
  IP SANs from the fixed `:9443` routes; Mac has no public listener or IP SAN.
- A host-local key store creates Ed25519 leaf keys with mode `0600` beneath a
  private host directory, refuses overwrite/symlink boundaries, and retains
  private material locally. The CSR is DER, signed by that leaf key, and has
  only the fixed common name, URI SAN, and required Ubuntu IP SAN.
- The constrained installer accepts only an opaque one-use mandate ID. Its
  mandate source, host attestor, and request outbox are interfaces; the caller
  cannot provide a path, CSR, SAN, certificate, or reusable key. The unit tests
  use an in-memory outbox only; they publish no mailbox or Runner request.
- The host-signed CSR request binds the host, role, management generation or
  exact bootstrap digest/generation/installer tuple, SANs, public-key
  fingerprint, CA profile, and bounded certificate validity. Approval,
  request, and issuer receipt signatures use separate domain strings.
- The issuer validates the approval signature and time window, host request
  signature, CSR signature and exact identity, public-key fingerprint,
  mandate context, CA profile, and bounded validity. It consumes each mandate
  once, issues only the local CSR public key, and returns a certificate/trust
  package plus signed non-secret receipt. The package schema has no private-key
  field.
- The bootstrap handoff carries only the host-signed public request, certificate
  and trust package, receipt, and pinned bootstrap digests. The fixture rejects
  a second handoff, a changed installer tuple, and bootstrap after a host's
  first health gate. The issuer also refuses a second bootstrap issuance for a
  host and closes bootstrap issuance after that health gate.
- The structural host-management package validator binds request ID, issuer
  receipt, local-key match, package digest, management manifest/generation, and
  active-peer-set digest. Canonical signed authorization enforcement and its
  durable state are supplied by L3.
- The active-peer TLS configs use TLS 1.3, standard certificate-chain
  verification and fixed literal-IP `ServerName`, then check URI/IP identity
  and exact active certificate/SPKI fingerprints. One distinct old leaf may be
  present during the bounded 14-day overlap; a chain-valid leaf removed from
  the active set is refused.

The `MemoryPKIIssuanceJournal` and `PKIBootstrapGate` are source fixtures for
the local unit; they are not production durable state. L3 must connect the
issuer and bootstrap gates to protected anchor state, signed canonical records,
and authenticated host/operator IPC before any live issuance or installation.
This L2 source acceptance does not claim a production issuer, durable mandate
ledger, actual outbox transport, root-console ceremony, package installation,
or real `:9443` reachability.

## Pre-unit inventory at E `ebaa5e7`

The PKI and bootstrap protocol in the detailed design, L2-04 handbook and
coverage rows, L2-03 route/trust implementation, management role asset
boundaries, and test dispatcher were read before implementation.

| Read input | Blob at predecessor E |
| --- | --- |
| `AGENTS.md` | `3402877131d9fcea513f5bae756582e4127ee892` |
| Detailed design | `f7404c2d5a0b35706f3a9e867991bffcb6171d93` |
| Phased plan | `082e73ac62a7815c3ed15778eb8162d81a43c347` |
| Execution handbook | `d04a659df5c5834725ea05b9b047f4ad296b21bc` |
| Execution ledger | `e0119dec86c2a3ba1694bd4d13d0d2ac3a646a7f` |
| L2-03 evidence | `3b80b51cea9ea88a88377225dc81dc4e8093269d` |
| Lane test coverage manifest | `1b1fcd776d6c6bd938d64f7c1512690302c520a6` |
| Management role package | `86d46c97ffdd19b7c4ca19bf85933b81379de2fc` |
| Direct/management TLS implementation | `c6302c51d6ccfe22b0771e61ab80ecc88d6871b9` |
| Test dispatcher and manifest checks | `e117190a0d8ebaed4cf1d28348787fe42c8933bf` |

## Tests and results at Source S

The tests generate temporary identities under `t.TempDir()`. The N assertion
binds local loopback listeners while setting TLS `ServerName` to each actual
fixed public IP. It uses normal Go certificate verification and opens no
connection to those remote addresses.

| Machine | H (5 assertions) | N (1 assertion) | `make check build` | Focused race | Final checkout |
| --- | --- | --- | --- | --- | --- |
| Mac `mac-control-plane` | PASS | PASS | PASS | PASS | clean `dev`, S |
| `linux-host` (`oracle-yuta-konopka-ubuntu-micro-02`) | PASS | PASS | PASS | not run on host | clean `dev`, S |
| `sandbox-host` (`oracle-gustaw-janecki-ubuntu-flex-02`) | PASS | PASS | PASS | not run on host | clean `dev`, S |
| `factory-sandbox` (`oracle-waclaw-dziwulski-ubuntu-micro-01`) | PASS | PASS | PASS | not run on host | clean `dev`, S |

All four machines used the pinned Go 1.27.1 toolchain and ran:

```sh
make test-lanes UNIT=LANES-L2-04 LANES_TIER=H
LANES_NATIVE_HOST_ID=<this-host> make test-lanes UNIT=LANES-L2-04 LANES_TIER=N
make check build
```

On the Mac, the focused race command also passed:

```sh
GOTOOLCHAIN=local go test -race ./src/internal/laneinstall ./src/internal/lanetests -run 'TestLanesL204|TestLanesL101ManifestMatchesPlan' -count=1
```

Mac used the Makefile's shared `GOCACHE=/private/tmp/remote-session-runner-gocache`
and `GOMODCACHE=/private/tmp/remote-session-runner-gomodcache`. Ubuntu reused
the selected account's normal Go caches. `make check build` passed full
`go test ./...`, `go vet ./...`, command smoke checks and command builds on all
four machines. `gofmt -d`, JSON parsing, and `git diff --check` passed on Mac.

The first Mac N run correctly rejected the fixture leaf because an initial
fixed fixture timestamp was ahead of the host clock. The fixture now uses the
current UTC clock. The final Mac and all Ubuntu N runs passed.

## Git delivery

The reviewed four-file source change was committed on Mac `dev` as S
`dcbe658572763c7b870a725193a2e078ae1ca28a`:

- source: `src/internal/laneinstall/pki.go`,
  `src/internal/laneinstall/lanes_l204_test.go`,
  `src/internal/lanetests/manifest_test.go`, and
  `040-implementation-evidence/LANES-test-coverage.json`;
- primary Gitea `origin/dev`: exact S;
- automatic mirror `github/dev`: exact S, verified by direct `ls-remote`;
- `linux-host`, `sandbox-host`, and `factory-sandbox`: clean `dev`, exact S,
  each pulled with `--ff-only` from the GitHub mirror and passed the gates above.

The sandbox denied the first ordinary Mac commit attempt at `.git/index.lock`;
the same reviewed commit succeeded with the authorized Git-write escalation.
The Gitea push delivered S, but Git could not update the local
`refs/remotes/origin/dev` tracking ref. Direct primary and mirror reads both
returned exact S; all three Ubuntu pulls and validations completed at S.

This evidence file and the L2-04 PASS row in the ledger are the docs-only
evidence closeout E. E is delivered separately through primary Gitea, exact
GitHub mirror verification, and clean fast-forward pulls on all three Ubuntu
hosts. E gates `LANES-L2-05`.

## Acceptance boundary

L2-04 is **PASS** for source acceptance. Local-key/CSR/issuance mechanics,
package binding, bootstrap refusal and active-peer TLS behavior are covered by
isolated tests. Installing real CA or leaf material, invoking the issuer,
publishing an outbox record, modifying trust, starting/reloading a listener,
or changing firewall/NAT state remains outside this source unit and requires
its later exact approval and dependency gates. No PRD material, service,
mailbox, held ACK, or retained BUG-021/B024 evidence was changed.
