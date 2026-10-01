# Phased implementation plan: malformed mailbox request feedback

**Status:** approved implementation plan on 2026-10-01. It extends the
completed configurable-inbox PoC and follows the original plan's serial gate,
evidence, and host-handoff rules.

## 1. Per-phase protocol

Before editing code for each phase, read through end-of-file:

1. the original initial design, detailed design, and phased plan;
2. the multiple-inbox idea, detailed design, and plan;
3. this idea, detailed design, and plan;
4. root `AGENTS.md`, current `README.md`, preimplementation decisions, prior
   phase evidence and diff, Git status and HEAD; and
5. every relevant source, test, schema, migration, configuration, deployment,
   and documentation file.

Record the complete read inventory, source HEAD, design/plan blobs, selected
machine, prerequisites, unresolved assumptions, and exact test gate in
`040-implementation-evidence/Pnnn.md` before code changes. Make every
versioned change in the Mac checkout. For each passing phase, inspect the
diff, commit it, push `dev` with the prescribed Mac key, fast-forward the
clean primary Ubuntu checkout with its prescribed key, and record matching
SHAs before host work or the next phase. Do not edit project files on Ubuntu.

Each phase runs its focused tests plus `make test`, `go test -race ./...`,
`make vet`, `make build`, `make smoke`, and `git diff --check` on the Mac
unless a test is explicitly inapplicable and recorded as such. A failed gate
stops the sequence. Physical power-loss durability remains unverified.

## 2. Serial phases

| Phase | Deliverable | Required focused gate |
| --- | --- | --- |
| `P164` | Add migration 30 and store-level append-only ingress-diagnostic ledger, frozen sanitized schema, fingerprint binding, projection/metadata cleanup eligibility, and migration/retention tests. No importer behavior changes yet. | Fresh and v29 database migrations; same fingerprint replay; changed fingerprint request-ID reuse; no raw request/idempotency/script persistence; artifact/metadata lifecycle test. |
| `P165` | Add trusted invalid-input classification, durable diagnostic intake/recovery, private projection, pair cleanup, safe duplicate handling, structured sanitized logging, fifth mailbox child preparation/validation, and read-only `mailboxclient` diagnostic support. | Native `0600` and workspace `0644` malformed/schema-invalid pairs; unsafe pairs remain inert; no exchange/intent/session/command/remote call; restart seams; adjacent valid work; no hot-loop writes/logs; exact `0600` diagnostic projection; client decoder; race suite. |
| `P166` | Deploy the Mac relay revision, perform a new harmless direct-workspace malformed-request acceptance, update post-implementation documentation and diagrams, and close BUG-006 with exact evidence. | New test request produces a private diagnostic and consumes its pair; no local or remote work record exists; service remains ready; a primary-Ubuntu source fast-forward matches the Mac commit. Do not replay either Logger workflow. |

Start `P165` only after `P164` PASS and its GitHub/Ubuntu handoff; start
`P166` only after `P165` PASS and its handoff. A source commit is not a live
acceptance result. The existing real Logger requests are preserved during
development; the new harmless request is the required first live proof.
