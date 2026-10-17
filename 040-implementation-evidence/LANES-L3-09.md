# LANES-L3-09 — constrained install, capacity, and ingress attestations

**State:** IN PROGRESS — L3-09 H and native N pass on Mac. Source S delivery
and the required synchronized Ubuntu validation are pending. This is source
evidence only; no attestation helper was installed and no live lane directive
or promotion was accepted.

## Pre-unit record and scope

- Pre-unit Mac `dev` HEAD: `d7506d02cd1077de94f30cf619844e87821f3007`, the
  documentation-only L3-08 closeout C. The worktree was clean before L3-09.
- L3-08 source S is `1844fb5abfd4dd5c6c7be731e5ac07fa9148c8ce`. Its H and
  full source gates passed on Mac and all three synchronized Ubuntu checkouts;
  its required F tier remains BLOCKED by the missing Linux A01/cache-backup
  devices and Mac APFS/cache-backup fixtures. Its evidence closeout E is not
  available. This unit depends on the delivered L3-08 source interface only;
  it does not use physical cache or backup devices.
- `LANES-L3-07` remains BLOCKED by the ordered L3-06/L2-09/L2-07/L2-08
  acceptance chain. Its delivered source and evidence remain preserved.
- The owner-authorized successive source work applies. B021/B024 maintenance,
  publisher pause, production credentials, a maintenance window, and service
  changes are not prerequisites for this unit. B021/B024 remain BLOCKED;
  BUG-022/023 remain deferred; held ACKs and the maintenance snapshot remain
  untouched.
- No production identity, mailbox, host storage, installed service, firewall,
  credential, or production Runner was read or changed. Test inputs use
  synthetic records and isolated fake observers/publishers. The N assertion
  reads only native platform facts from the fixed `/` filesystem path.

## Read inputs at the predecessor

The design, plan, handbook, `AGENTS.md`, ledger, L3-07/L3-08 evidence,
coverage contract, payload validators, security anchor/endpoint, and capacity
model were reviewed at pre-unit HEAD `d7506d02cd1077de94f30cf619844e87821f3007`.

| Input | Blob at pre-unit HEAD |
| --- | --- |
| `AGENTS.md` | `3402877131d9fcea513f5bae756582e4127ee892` |
| Lane detailed design | `f7404c2d5a0b35706f3a9e867991bffcb6171d93` |
| Lane phased plan | `082e73ac62a7815c3ed15778eb8162d81a43c347` |
| Lane execution handbook | `d04a659df5c5834725ea05b9b047f4ad296b21bc` |
| Execution ledger | `59fc710a8297c8c633349587a7b91941c90320c1` |
| L3-07 evidence | `a55480139bebca1ee9013d3cedcc11e614012dda` |
| L3-08 evidence | `2e96ce8244c0deb732b587e6ee1ca1fc6527e8b3` |
| Lane coverage manifest | `0a73bae1fe3ebc1594161cc04ae2fa5b14846996` |
| `release/l3_payload.go` | `f3cb21f3b0e5e8012face0592bdaae2962a25413` |
| `release/security_endpoint.go` | `bf09eccd271bbcc20e37bd31a054dba4447623ea` |
| `release/security_anchor.go` | `e41b0fb24e739ad0eecee7470d2c9b9cd5fbd698` |
| `lanecapacity/policy.go` | `fab5a8c899bdbdd8f44938f3c2aa77ab5fe89e7a` |
| `lanetests/manifest_test.go` | `94aed201f75a98b1b27180c2364d078021d67f48` |

## Source implementation

`HostAttestationService` binds construction to a root-opened security anchor
and checks that the anchor host matches the native OS/architecture. Its public
operations accept only an empty capacity request, an opaque promotion
authorization ID, or no ingress arguments. The evidence source contract reads
capacity, verified authorization/directive records, installed state, and exact
ingress surfaces locally; it accepts no path, capacity value, or surface list
from a caller. The publisher contract receives only the closed payload and
fixed record type.

Capacity attestations bind the complete `lanecapacity.Measurement` digest to
the anchored host, sorted safe evidence references, fresh observation time,
and still-valid measurement expiry. Promotion attestations are reconstructed
from a verified host-bound authorization and local installation observation,
then validated against the full authorized component set and transition tuple.
Ingress attestations are reconstructed from the currently applied verified
directive and require every host-specific surface in canonical order, a safe
evidence digest per surface, and no `applied` result after directive expiry.
Invalid, stale, foreign-host, incomplete, or inconsistent observations are not
published.

The native N assertion measures the actual OS/architecture, CPU count, root
device, and root filesystem capacity from the current host. This is a platform
identity check, not a Dev/QAS/PRD capacity measurement or a live ingress proof.
Production measurement/installation observers and the root signer/outbox are
trusted internal boundaries used by this service; their installation and live
operation belong to later prescribed units.

## Mac source gates

Execution machine: `mac-control-plane` (Darwin arm64), Makefile-pinned Go
1.27.1, shared Mac Go caches.

| Command | Result |
| --- | --- |
| `make test-lanes UNIT=LANES-L3-09 LANES_TIER=H` | PASS — 7 registered assertions |
| `LANES_NATIVE_HOST_ID=mac-control-plane make test-lanes UNIT=LANES-L3-09 LANES_TIER=N` | PASS — 1 registered native assertion |
| `make check build` | PASS — full tests, vet, and command builds |
| `go test -race ./src/internal/release -run '^TestLanesL309' -count=1` | PASS |
| pinned `gofmt`, coverage JSON parse, `git diff --check` | PASS |

One initial full-suite run found the N assertion failed when invoked outside
the N-tier dispatcher because it required native-tier environment variables.
The test now skips outside that dispatcher; the registered N tier and full
source suite both pass. No failure was waived.

## Delivery

Source S and post-push validation are pending. The completed source commit will
be pushed to primary Gitea, verified at the exact automatic GitHub mirror SHA,
and fast-forwarded to all three clean Ubuntu `dev` checkouts before their
registered L3-09 gates run. This unit remains subject to the ordered L3-08
acceptance blocker; no PASS or evidence closeout E is claimed.
