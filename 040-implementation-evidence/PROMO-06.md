# PROMO-06 — source markers and operator documentation

**Status:** PASS — implementation, fixture tests, and documentation.

- Source implementation commit: `1979fa6d247602af1aad3370d277c9c1c403377e`.
- `qas` and `prd` marker synchronization is separated from runtime activation;
  compare-and-swap/fast-forward constraints, mirror lag, divergence, retry
  after accepted runtime, and Git-only recovery are covered by disposable
  primary/mirror tests.
- Updated `AGENTS.md`, the catalog design/handbook overlays, `docs/lanes.md`,
  and `docs/promotion-guide.md` to match the CLI and staged walkthrough.
- `PROMO-08` is explicitly NOT RUN. No lane branch was advanced, no service
  was deployed/restarted, and no mailbox request or ACK was published.

Test and delivery details: [PROMO-07](PROMO-07.md).
