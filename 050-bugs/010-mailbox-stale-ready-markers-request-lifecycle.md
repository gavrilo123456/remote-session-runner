# BUG-010 — Mailbox orphan `.ready` markers inflate backlog and hide request lifecycle

## Summary

| Field | Value |
| --- | --- |
| Status | `IMPLEMENTING — B010-P1 and B010-P2 PASS; B010-P3 through B010-P4 pending` |
| Severity | High |
| Priority | High — the file-only client can mistake inert residue for pending protected-control work |
| Reported | 2026-10-02 |
| Discovered by | SlideStudio operator / Codex mailbox client |
| Owner | Unassigned |
| Affected component/path | Mac `runner-local`, workspace mailbox lifecycle, metrics, and `/Users/tomasz.walczuk/projects/slidestud.io/tmp/mailbox-/` |
| Affected live revision | Mac relay `9ad2fb01d85952161cbbc387188b3fc774a103f1`; P165 malformed-ingress behavior is included |
| Checked-out source | `9aa08cd2de5de1b7d63b072a1ce23fd5b66e13a6` (B010-P2) |
| Fixed revision | N/A |
| Verification | B010-P1 through B010-P4 below; no current mailbox artifact is to be deleted, ACKed, retried, or replayed during investigation |

## Confirmed problem

The SlideStudio mailbox has orphan request and ACK `.ready` markers: safe
zero-byte markers whose paired JSON file is absent. They are **inert**, so they
cannot dispatch work, but the current metric counts request markers as mailbox
backlog and the client has no supported read-only way to classify them.

This is a lifecycle visibility and cleanup problem. It is not evidence of
currently pending remote commands or a terminal-result retention mechanism.

## Current read-only inventory

Inventory time: 2026-10-02. Root:

```text
/Users/tomasz.walczuk/projects/slidestud.io/tmp/mailbox-
```

| Area | Count | Observed state |
| --- | ---: | --- |
| `inbox/` | 51 | 50 zero-byte, exact-`0644` request markers without JSON; one JSON-only draft |
| `outbox/` | 247 | Existing normal mailbox response projections |
| `acks/` | 43 | Zero-byte, exact-`0644` ACK markers without ACK JSON |
| `diagnostics/` | 2 | Private safe-ingress rejection records |
| `events/` | 244 | Existing event projections |

The 50 request markers were all correlated without reading scripts, command
output, credentials, or raw idempotency keys:

| Classification | Count | Evidence |
| --- | ---: | --- |
| Terminal response and terminal event; matching orphan ACK marker also exists | 41 | `outbox/<request_id>.json`, event file, and `acks/<request_id>.ready` |
| Terminal response and terminal event; no current ACK marker | 9 | `outbox/<request_id>.json` and event file |
| Draft JSON only | 1 | `inbox/req-codex-read-logger-g1-log-ranges-20261002-154.json`; no `.ready`, outbox, diagnostic, or command |

The 41 ACK markers are **not proof of acknowledgement**: their matching ACK
JSON files are absent, so the response revision and event cursor cannot be
verified from the filesystem pair. The durable authority is the only source of
truth for acknowledgement.

The nine terminal rows without a current ACK marker consist of six `lost` and
three `succeeded` commands. They must remain correlated through their existing
outbox/event records; no inference-based ACK, retry, or replay is safe.

Two safe ingress diagnostics exist:

```text
diagnostics/req-codex-logger-migration-review-controller-20261001-02.json
diagnostics/req-p166-malformed-20261001-01.json
```

Both state `accepted=false`, `executed=false`,
`lifecycle_phase=ingress_validation`, and `code=invalid_request_schema`.

The Mac service was live and ready during the inventory:

```text
active session slots: 0
active command slots: 0
queued commands:      0
queued intents:       0
mailbox_backlog:      50
```

The value `50` exactly matched the orphan request-marker count. It is therefore
a false actionable-work signal, not a remote queue backlog.

## Actual lifecycle contract

The supported request lifecycle is:

```text
draft JSON
  -> safe published JSON + empty marker
  -> durable exchange receipt
  -> accepted/progress outbox
  -> terminal outbox + events
  -> optional durable ACK
  -> retained response/event artifacts
  -> configured artifact cleanup

safe complete pair that fails ingress validation
  -> private diagnostic
  -> input-pair cleanup

missing, nonempty, unsafe, or incomplete pair
  -> inert
  -> no diagnostic and no remote work
```

A normal accepted request does **not** retain an inbox marker. The current
importer writes the durable exchange receipt before the operation boundary and
then removes the request pair in marker-first order. Terminal retention belongs
to `outbox/` and `events/`; a valid ACK is recorded before its ACK pair is
removed.

An orphan marker is thus neither pending work nor terminal retention. A
restart cannot replay it because the required JSON member is missing.

## Investigation findings

### The historical one-byte marker case

`req-codex-logger-migration-review-retry-20261001-01` initially had a
one-byte marker. That pair was inert: it was not a durable rejection and did
not produce a diagnostic or remote command. Its later lifecycle has terminal
proof:

```text
request_state: complete
command_state: lost
event terminal type: command_lost
response_revision: 3
```

The current relay performs a complete scan of every configured inbox every
250 ms; it does not depend only on a filesystem creation event. P165 already
proves re-evaluation after a nonempty marker is atomically replaced by a
zero-byte marker. The exact same-inode one-byte-to-zero-byte truncate used by
the former editor workaround is not covered by a dedicated regression and
belongs in B010-P2 if that publisher behavior remains supported.

A permanently nonempty, unsafe, or missing pair must remain inert. It must
not be turned into a diagnostic merely to make it visible: safe diagnostics
require a safe complete pair that can be fingerprinted without retaining
untrusted input.

### Why the current residue cannot be attributed to normal Runner cleanup

The live `9ad2fb0` source contains P165 and removes request and ACK pairs in
this order:

```text
.ready -> directory sync -> .json -> directory sync
```

That ordering cannot normally leave a marker while deleting its matching JSON.
The same is true for valid ACK pairs. The existing 50 request-marker-only and
43 ACK-marker-only artifacts therefore require a JSON-only deletion or a
later marker creation outside the normal current consumer path.

There is no retained filesystem audit identifying that writer, so this record
does not assign blame to a particular client or earlier service revision.

### Confirmed product gaps

1. `ReadyRequestCount` counts safe-mode marker files without requiring a
   zero-byte marker and matching safe JSON. Orphan or unsafe markers can inflate
   `mailbox_backlog`.
2. The regular relay discards non-durable rejected importer results. An orphan
   marker has no client-visible lifecycle classification or useful bounded log.
3. There is no read-only, mailbox-scoped command/API to correlate filesystem
   shape with the durable exchange, acknowledgement, and diagnostic records.
4. Current cleanup removes unmarked old JSON drafts and expired response/event
   artifacts, but deliberately leaves marker-only artifacts for operator
   review. That is safe, but currently opaque.

## Scope and safety constraints

- Preserve native exact-`0600` and direct workspace exact-`0644` ingress
  modes. Runner-produced outbox, event, and diagnostic files remain private
  exact-`0600`.
- Never emit request JSON, scripts, command output, idempotency keys, token
  values, headers, private-key material, or raw filesystem errors through
  status, metrics, or logs.
- Keep lookup mailbox-scoped. The same request ID in another configured root
  is unrelated.
- Do not add a second mailbox receipt ledger. The existing durable exchange
  and acknowledgement records remain authoritative.
- Do not turn marker-only, nonempty, unsafe, or incomplete input into remote
  work, an artificial terminal response, or a safe-ingress diagnostic.
- Do not automatically delete an orphan with no matching durable record.
  It may be an unsupported publisher race and must remain inert for review.
- Do not delete, ACK, retry, or replay the current SlideStudio artifacts while
  performing source work or automated tests.

## Fix plan

### Delivery discipline for every phase

Before each phase, reread this record, `AGENTS.md`, `docs/mailbox.md`,
`docs/operations.md`, and the source/tests named for that phase. Make all
versioned changes in the Mac checkout only. For every completed phase: inspect
the diff, pass its gates, commit it on `dev`, push it with the specified Mac
GitHub key, fast-forward both Ubuntu checkouts with their specified key, and
prove the same commit before remote validation or phase N+1.

No phase may be marked complete from a source-only check. Preserve unrelated
worktree changes and keep current mailbox artifacts untouched unless a later,
explicitly reviewed live-cleanup step authorizes their removal.

### B010-P1 — Read-only classifier and truthful backlog

**Goal:** make mailbox state explainable without making filesystem residue an
execution authority.

Implement one internal mailbox-scoped lifecycle classifier using only safe
request IDs, `lstat` metadata, the existing AuthorityStore exchange/
acknowledgement/diagnostic records, and no request-body reads. It must report
two independent dimensions:

| Dimension | Values |
| --- | --- |
| Input shape | `publishable_pair`, `json_draft`, `request_marker_only`, `ack_marker_only`, `unsafe_inert` |
| Durable state | `none`, `accepted`, `terminal_unacknowledged`, `terminal_acknowledged`, `ingress_diagnostic` |

It must also return a safe action:

| Action | Meaning |
| --- | --- |
| `none` | Normal state or no safe action |
| `eligible_durable_orphan_cleanup` | A marker-only artifact maps to existing durable evidence and can be considered by B010-P2 |
| `retain_unproven_inert` | No durable record proves ownership; leave it inert |

Replace the marker-only backlog count with:

```text
durable accepted exchanges
+ safe empty request marker with a safe matching JSON and no durable
  exchange/diagnostic record yet
```

This counts actionable work once and excludes marker-only, nonempty, symlink,
unsafe-mode, and already-durable artifacts. Keep the existing metric name;
document its corrected meaning and expose inert counts only through the
classifier.

**Automated gates**

1. Safe `0600` and `0644` complete pairs are classified as publishable and
   counted.
2. A durable accepted exchange with a retained pair is counted once only.
3. Terminal, rejected, diagnostic, marker-only, nonempty, symlink, unsafe-mode,
   and cross-mailbox same-ID cases are excluded from actionable backlog.
4. Request-marker-only and ACK-marker-only states distinguish matching durable
   evidence from no evidence without reading or exposing payloads.
5. Focused tests, then `make test` and `make vet` on the Mac.

### B010-P2 — Bounded durable-orphan reconciliation

**Goal:** remove only residue that durable evidence proves is non-executable.

After normal request and ACK intake, add a bounded reconciliation pass using
the B010-P1 classifier. It is deliberately disabled unless the configured
mailbox sets `durable_orphan_cleanup: true`; omitted is `false`. This keeps
existing reviewed residue intact through the B010-P3 status review and makes
later cleanup an explicit mailbox-owner action. When enabled, it may remove
**only the marker file** when all of the following are true:

- it is a safe regular exact-`0600` or exact-`0644` zero-byte marker;
- its paired JSON is still absent immediately before unlink;
- a request marker maps to an existing durable exchange or ingress diagnostic,
  or an ACK marker maps to a durably acknowledged exchange; and
- the marker is revalidated and the directory is synced during removal.

It must not remove a JSON draft, an unproven marker-only artifact, a nonempty
or unsafe entry, a response, an event file, a diagnostic record, or an ACK/
exchange record. It must not call SessionOperations, a remote bridge, or any
remote mutation.

**Automated gates**

1. Known terminal, lost, diagnostic, and durably acknowledged marker-only
   residue is removed without changing response/event/acknowledgement state.
2. An unknown marker-only artifact survives repeated cycles and restart,
   remains inert, is excluded from backlog, and causes no execution.
3. Concurrent JSON/marker replacement races leave the newer publisher input
   untouched and never dispatch it early.
4. A one-byte marker creates no work or diagnostic; a later safe zero-byte
   marker is re-evaluated exactly once. Cover the same-inode truncate variant
   if the direct workspace publisher continues to use it.
5. An unsafe replacement remains inert while independent fresh work progresses.
6. Omitted configuration leaves reconciliation inactive; an explicit per-inbox
   opt-in activates it only for that mailbox.
7. Focused tests, then `make test` and `make vet` on the Mac.

### B010-P3 — Read-only operator status and documentation

**Goal:** give an operator or scoped LLM client deterministic lifecycle lookup
without SQLite inspection.

Add a narrow read-only query to the existing owner-only local API, selected by
configured `inbox_id` and optionally a safe `request_id`. It returns only the
B010-P1 labels, safe stable IDs, terminal/acknowledgement flags, and aggregate
counts. It must reject unknown inboxes, avoid arbitrary paths, redact all
payload material, and return an explicit unavailable result rather than
falling back to marker inference. Do not use `runner-local doctor` because it
writes a health record.

Update `docs/mailbox.md`, `docs/operations.md`, `docs/llm-client-guide.md`,
and `docs/README.md` to explain:

- a marker alone never proves pending or accepted work;
- request ID → outbox → command ID → events remains the normal correlation
  chain;
- terminal retention belongs to outbox/events, not inbox;
- valid ACK evidence comes from the durable record, not a lonely marker;
- the new status query and `retain_unproven_inert` handling;
- corrected `mailbox_backlog` semantics and all existing retention limits.

**Automated gates**

1. Local API authorization and JSON-contract tests.
2. Redaction tests prove that the query neither reads nor returns a request
   body, script, output, idempotency key, token, header, or key material.
3. External exact-`0644` mailbox coverage and read-only no-write regression.
4. Focused tests, then `make test`, `make vet`, and `make build` on the Mac.

### B010-P4 — Delivery and live proof

**Goal:** prove the Mac ingress fix without replaying Logger work.

1. Follow the delivery discipline above for the B010 source commit(s).
2. Refresh only the Mac `runner-local` installation after its live
   `build_revision` matches the pushed source. The sandbox Runner and bridge do
   not need a code restart for a Mac-only lifecycle change unless the phase
   evidence identifies a dependency.
3. Publish one new harmless direct-workspace SlideStudio request, wait for its
   terminal result, inspect lifecycle status, ACK it, and prove it does not
   leave actionable backlog or replay after a Mac service restart.
4. Run the applicable workspace-mailbox host gate and sandbox P128 zero-work
   status. Do not mark a new host gate passed from this evidence.
5. Run lifecycle status against the existing 50 artifacts. Only after its
   output is reviewed may a separately authorized change set
   `durable_orphan_cleanup: true` for that mailbox and refresh the Mac service.
   The bounded B010-P2 pass may then remove only rows with durable proof.
   Preserve every `retain_unproven_inert` row and do not replay Logger
   workflows.
6. Record Mac/Ubuntu commit parity, installed Mac build revision, commands,
   sanitized status evidence, and host evidence in implementation evidence.

## Acceptance criteria

BUG-010 can be resolved only when all of the following hold:

1. `mailbox_backlog` reflects actionable unpublished/accepted work and excludes
   the current class of inert orphan markers.
2. A normal valid pair creates one durable exchange and one operation, removes
   its input pair, and cannot replay after restart.
3. Safe malformed complete pairs retain the existing private diagnostic path;
   missing, nonempty, and unsafe pairs remain inert with no work.
4. A valid ACK is durable before its pair disappears; an ACK marker without its
   JSON is not reported as acknowledgement.
5. Operator status gives an unambiguous mailbox-scoped classification without
   exposing sensitive payload data or requiring a SQLite query.
6. Bounded reconciliation removes only marker-only artifacts tied to durable
   evidence and never modifies unproven input or remote work.
7. Documentation states the exact lifecycle, status lookup, cleanup boundaries,
   and retention limits.

## Resolution

Open. The current 50 orphan markers are classified as inert and not pending
execution. They remain preserved until the planned implementation and an
explicitly reviewed cleanup step are complete.

## History

| Date | Change |
| --- | --- |
| 2026-10-02 | Initial recurrence report created. |
| 2026-10-02 | Read-only source, runtime, and full mailbox inventory completed; record refined and B010-P1 through B010-P4 plan added. |
| 2026-10-02 | B010-P1 committed, pushed, and fast-forwarded to both Ubuntu checkouts; see `040-implementation-evidence/BUG-010.md`. |
| 2026-10-02 | B010-P2 committed, pushed, and fast-forwarded to both Ubuntu checkouts; it is disabled by default per inbox until B010-P3 status review and separately authorized live activation. |
