# LANES-L1-02 — profiles, contexts, endpoints, controllers and credential binding

**State:** PASS — the L1-02 hermetic source contract and exact-S delivery were
verified on Mac and all three Ubuntu `dev` checkouts. This is source-unit
acceptance only; it does not install a lane, validate real certificates, close
B021/B024, or claim live acceptance.

## Predecessor and independent scope

- Source predecessor: L1-01 evidence closeout `b25468dd09a63a9c85daee2b219dfc61f2adb492`, clean and synchronized on Mac `dev`. This unit is the next source unit in the prescribed L1 sequence.
- `LANES-B021-02` remains **BLOCKED**. Its evidence blob was `3e9700485f48ad75ad42ba04334d5b3871a36c7a` both at the predecessor and L1-02 S. `LANES-B024-02` remains **BLOCKED** with evidence blob `15211861260284d0923c9d64259bd7746700399e` at both revisions. The exact-pair recovery, maintenance, publisher pause, and acceptance gates were not prerequisites for this source-only unit and were not attempted.
- BUG-022/023 baseline units remain deferred/open. No baseline repair campaign was started.
- The existing external B021 maintenance package remains a snapshot, unchanged at SHA-256 `8bb177560880c1d3ec95ae7fe6dd0ac7cf1deb7e07abf2c18a048e3531d40360`; it was not rebuilt or used to freeze development.
- L1-02 used synthetic test fingerprints and deliberately nonexistent credential paths. No production credentials were read or needed. No mailbox request or ACK was published; no service, host configuration, storage, firewall, publisher, or production Runner was changed.

## Read inputs at the predecessor

The amended plan, handbook, ledger, `AGENTS.md`, and L1-01 closeout were
reloaded at predecessor `b25468dd09a63a9c85daee2b219dfc61f2adb492` before
implementation. Relevant blob IDs:

| Input | Blob |
| --- | --- |
| `AGENTS.md` | `3402877131d9fcea513f5bae756582e4127ee892` |
| Lane detailed design | `f7404c2d5a0b35706f3a9e867991bffcb6171d93` |
| Lane phased plan | `082e73ac62a7815c3ed15778eb8162d81a43c347` |
| Lane execution handbook | `d04a659df5c5834725ea05b9b047f4ad296b21bc` |
| Lane execution ledger | `3041ae19e9367b31dbf5b452abd8c389a09b8dd1` |
| L1-01 closeout | `b733031d71ed5943954cb8bd47b29632cae91c0c` |
| Blocked B021-02 evidence | `3e9700485f48ad75ad42ba04334d5b3871a36c7a` |
| Blocked B024-02 evidence | `15211861260284d0923c9d64259bd7746700399e` |

## Implemented contract

- Added a strict V3 profile/context YAML subset and JSON Schema. V1/V2 parsing remains unchanged; the general loader still does not claim this subset is a complete V3 runtime configuration.
- Mac bindings require all three fixed Ubuntu profiles. Linux bindings require exactly the local remote profile. Both bind queue and direct endpoints, listener addresses/ports, fixed socket, profile-derived secret paths, controller IDs, URI SAN claims, and safe SHA-256 fingerprints to `release_lane`.
- Each allowed environment target has exactly one named execution context, every context resolves to a permitted pair, every configured Mac route is used, and Linux cannot select a different local profile. Dev/QAS local execution is rejected; PRD retains a Mac-local compatibility context.
- Controller authorization is derived per pair: the queued Mac identity and the exact `runner-controller-<lane>-<profile>` direct-mTLS identity. The Mac/Linux pair validator cross-checks host-key and dispatcher-key fingerprints, direct client/server certificate fingerprints, CA fingerprints, URI SANs, and controller identity.
- Cross-lane checks reject reused host keys, dispatcher keys, client leaves, server leaves, and CA bundles. CA bundles may be shared by profiles within one lane. Fingerprints are assertions only; actual certificate and key inspection is a later cryptographic/install gate.
- Credential paths are validated as exact derived strings. The parser performs no filesystem reads. Tests use nonexistent paths and synthetic fingerprints, so they cannot access production credentials.
- Added six registered H assertions covering all lanes/profiles, Mac/Linux pairing, cross-lane credential separation, unsafe/cross-lane rejection, strict YAML, no credential-path reads, and schema closure. Updated the dispatcher test to retain a genuinely unregistered unit for the missing-assertion regression.

## Source gates on Mac

Commands ran on `mac-control-plane` (Darwin arm64), using Go 1.27.1 and the
Makefile's shared `GOCACHE=/private/tmp/remote-session-runner-gocache` and
`GOMODCACHE=/private/tmp/remote-session-runner-gomodcache`.

| Command | Result |
| --- | --- |
| `make test-lanes UNIT=LANES-L1-02` | PASS; all 6 registered H assertions ran and passed |
| `make test` | PASS; complete `./...` suite |
| `make vet` | PASS |
| `make build` | PASS |
| `make smoke` | PASS |
| Focused race: `go test -race ./src/internal/config ./src/internal/lanetests -run '^TestLanesL10[12]' -count=1` | PASS with the Makefile-shared Go caches |
| `gofmt`, JSON schema parse, `git diff --check` | PASS |

The restricted Mac shell initially failed the pre-existing
`TestBUG020MacShellExitWithInheritedControlWriterSettlesOnce`: `/bin/ps`
returned `operation not permitted`. The same full suite passed from the
previously authorized Mac shell. The first ad-hoc race invocation also tried
the restricted default Go cache and was denied; the focused race rerun used the
Makefile's shared caches and passed. These were execution-context failures,
not suppressed test failures.

## Source S and synchronized host validation

Source S is `fbb866789a01f7dcc442164c51ccf255b95405db`, committed on Mac `dev`
as `phase(LANES-L1-02): bind lane profiles contexts and credentials`. Primary
Gitea `origin/dev` and automatic GitHub mirror `github/dev` both resolved to
that exact SHA. Mac `dev` was clean at S.

All three Ubuntu checkouts were confirmed clean on `dev` before pulling, with
`origin=git@github.com:gavrilo123456/remote-session-runner.git` and prior HEAD
`b25468dd09a63a9c85daee2b219dfc61f2adb492`. Each fast-forwarded with
`git -c core.sshCommand='ssh -i /home/ubuntu/.ssh/gavrilo123456-github -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' pull --ff-only origin dev`.
Configured explicit Mac SSH identities and verified host keys were used. No
tracked source was edited or copied on Ubuntu.

At exact S, each Ubuntu host ran `make test-lanes UNIT=LANES-L1-02`, `make
test`, `make vet`, `make build`, and `make smoke`. The two hosts with cgo and a
C compiler also ran the focused race test on the changed config and lane-test
packages using `GOTOOLCHAIN=local /home/ubuntu/.local/share/remote-session-runner/toolchains/go1.27.1/bin/go test -race ./src/internal/config ./src/internal/lanetests -run '^TestLanesL10[12]' -count=1`. Results:

| Machine | Platform | L1-02 H / full / vet / build / smoke | Focused race | Final checkout |
| --- | --- | --- | --- | --- |
| `linux-host` | Linux amd64, Go 1.27.1 | PASS | PASS | `dev`, clean, HEAD S |
| `sandbox-host` | Linux arm64, Go 1.27.1 | PASS | PASS | `dev`, clean, HEAD S |
| `factory-sandbox` | Linux amd64, Go 1.27.1 | PASS | NOT RUN — `CGO_ENABLED=0`, no `cc` | `dev`, clean, HEAD S |

The first linux-host and factory-sandbox result-print step stopped after the H
gate because those hosts do not have `rg`; the H gate itself had passed. It was
rerun using `tail`, and each host completed the full listed sequence. No source
or test failure was observed.

Runner was not used: these are checkout-local Go commands, not mailbox work.
The owner-authorized SSH fallback ran the tests/builds on clean, synchronized
checkouts, using the hosts' configured explicit SSH identities. No request or
ACK publication, credential access, installed-service operation, or runtime
acceptance was performed.

The evidence/ledger closeout is delivered separately after S under the
repository's required S → primary push → exact mirror → Ubuntu fast-forward
sequence. No later L1 unit is included in this source commit.
