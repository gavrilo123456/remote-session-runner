# PROMO-05 — durable lane operation, resume, and rollback

**Status:** PASS — disposable four-host public-command qualification.

- Source implementation commit: `1979fa6d247602af1aad3370d277c9c1c403377e`.
- The shared HOST/all state machine persists intent before effects, synchronizes
  receipts, advances only the next host, keeps admissions held on partial
  cohorts, supports bounded evidence continuation, and performs compatible
  reverse-order rollback without restoring databases.
- Public subprocess fixtures exercise successful host/all completion,
  interruptions around switch and synchronization, retries/resume, expiry,
  partial receipt synchronization, wrong order, competing operations, and
  rollback. Mac full-suite results are recorded in [PROMO-07](PROMO-07.md).
- No live operation was started; actual host receipt or selector state was not
  read or changed by this source task.
