package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"remote-session-runner/src/internal/domain"
)

const (
	// MailboxIngressDiagnosticArtifactLifetime is the private-file retention
	// boundary for an unacknowledged ingress diagnostic. The durable ledger is
	// retained through DefaultMetadataRetention to prevent unsafe ID reuse.
	MailboxIngressDiagnosticArtifactLifetime     = MailboxUnackedResponseLifetime
	defaultMailboxIngressDiagnosticRecoveryLimit = 64
)

var (
	ErrMailboxIngressDiagnosticInvalid      = errors.New("mailbox ingress diagnostic is invalid")
	ErrMailboxIngressDiagnosticNotFound     = errors.New("mailbox ingress diagnostic not found")
	ErrMailboxIngressDiagnosticConflict     = errors.New("mailbox ingress diagnostic conflicts with mailbox exchange")
	ErrMailboxIngressDiagnosticExpired      = errors.New("mailbox ingress diagnostic projection has expired")
	ErrMailboxIngressDiagnosticCleanupState = errors.New("mailbox ingress diagnostic cleanup state is invalid")
)

// MailboxIngressDiagnosticCode is a fixed, sanitized ingress-validation
// outcome. Its value is never derived from an error string or request field.
type MailboxIngressDiagnosticCode string

const (
	MailboxIngressDiagnosticMalformedJSON              MailboxIngressDiagnosticCode = "malformed_json"
	MailboxIngressDiagnosticInvalidRequestSchema       MailboxIngressDiagnosticCode = "invalid_request_schema"
	MailboxIngressDiagnosticRequestIdentityMismatch    MailboxIngressDiagnosticCode = "request_identity_mismatch"
	MailboxIngressDiagnosticInvalidScript              MailboxIngressDiagnosticCode = "invalid_script"
	MailboxIngressDiagnosticRequestTooLarge            MailboxIngressDiagnosticCode = "request_too_large"
	MailboxIngressDiagnosticRequestIDReusedAfterReject MailboxIngressDiagnosticCode = "request_id_reused_after_rejection"
)

// MailboxIngressDiagnosticRef names an ingress-validation record without
// representing a normal mailbox exchange.
type MailboxIngressDiagnosticRef struct {
	MailboxID       string
	ClientRequestID string
}

// NewMailboxIngressDiagnosticRef validates a trusted mailbox namespace and
// filename-derived request ID. It deliberately shares the bounded identifier
// rules with mailbox exchanges without sharing their lifecycle.
func NewMailboxIngressDiagnosticRef(mailboxID, clientRequestID string) (MailboxIngressDiagnosticRef, error) {
	ref, err := NewMailboxExchangeRef(mailboxID, clientRequestID)
	if err != nil {
		return MailboxIngressDiagnosticRef{}, ErrMailboxIngressDiagnosticInvalid
	}
	return MailboxIngressDiagnosticRef{MailboxID: ref.MailboxID, ClientRequestID: ref.ClientRequestID}, nil
}

func validateMailboxIngressDiagnosticRef(ref MailboxIngressDiagnosticRef) (MailboxIngressDiagnosticRef, error) {
	return NewMailboxIngressDiagnosticRef(ref.MailboxID, ref.ClientRequestID)
}

// MailboxIngressDiagnosticDisposition distinguishes a new safe rejection, a
// restart-safe replay of the original pair, and a later request-ID reuse.
type MailboxIngressDiagnosticDisposition string

const (
	MailboxIngressDiagnosticCreated         MailboxIngressDiagnosticDisposition = "created"
	MailboxIngressDiagnosticSameFingerprint MailboxIngressDiagnosticDisposition = "same_fingerprint"
	MailboxIngressDiagnosticRequestIDReused MailboxIngressDiagnosticDisposition = "request_id_reused_after_rejection"
)

// MailboxIngressDiagnosticRecord contains only trusted identity, a raw-byte
// fingerprint, frozen sanitized JSON, and lifecycle timestamps. It never
// retains the raw request, script, operation, idempotency key, target, or an
// untrusted parser error.
type MailboxIngressDiagnosticRecord struct {
	MailboxID                  string
	RequestID                  string
	RequestSHA256              [sha256.Size]byte
	Code                       MailboxIngressDiagnosticCode
	DiagnosticRevision         int64
	DiagnosticBytes            []byte
	DiagnosticSHA256           [sha256.Size]byte
	ObservedAt                 time.Time
	ProjectedAt                *time.Time
	InputCleanupStartedAt      *time.Time
	InputPairRemovedAt         *time.Time
	DiagnosticCleanupAt        time.Time
	DiagnosticCleanupStartedAt *time.Time
	DiagnosticFileRemovedAt    *time.Time
}

// MailboxIngressSchemaDetail is a bounded, request-content-free correction
// hint for a v1 schema rejection. It is intentionally restricted to static
// protocol terms: it never records a script, field value, target, token, or
// parser error from the rejected request.
type MailboxIngressSchemaDetail struct {
	SchemaVersion       string
	JSONPointer         string
	Expected            string
	ReceivedType        string
	CanonicalRunField   string
	CanonicalRunType    string
	MinimalRunRequestID string
	MinimalRunKey       string
	MinimalRunOperation string
	MinimalRunScript    string
}

type mailboxIngressDiagnosticWire struct {
	InboxID            string                          `json:"inbox_id"`
	RequestID          string                          `json:"request_id"`
	DiagnosticRevision int64                           `json:"diagnostic_revision"`
	LifecyclePhase     string                          `json:"lifecycle_phase"`
	Accepted           bool                            `json:"accepted"`
	Executed           bool                            `json:"executed"`
	Code               MailboxIngressDiagnosticCode    `json:"code"`
	Message            string                          `json:"message"`
	SchemaDetail       *mailboxIngressSchemaDetailWire `json:"schema_detail,omitempty"`
	ObservedAt         string                          `json:"observed_at"`
}

type mailboxIngressSchemaDetailWire struct {
	SchemaVersion     string `json:"schema_version"`
	JSONPointer       string `json:"json_pointer"`
	Expected          string `json:"expected"`
	ReceivedType      string `json:"received_type"`
	CanonicalRunField string `json:"canonical_run_field"`
	CanonicalRunType  string `json:"canonical_run_type"`
	MinimalValidRun   struct {
		RequestID      string `json:"request_id"`
		IdempotencyKey string `json:"idempotency_key"`
		Operation      string `json:"operation"`
		Script         string `json:"script"`
	} `json:"minimal_valid_run"`
}

// RecordMailboxIngressDiagnosticInMailbox creates or reloads a frozen
// malformed-input record. The caller provides only a SHA-256 fingerprint and
// fixed code, never the raw request. A later changed fingerprint, or a pair
// re-published after its original pair was removed, cannot replace the
// diagnostic and is reported as retained-ID reuse.
func (s *AuthorityStore) RecordMailboxIngressDiagnosticInMailbox(ctx context.Context, ref MailboxIngressDiagnosticRef, requestSHA256 [sha256.Size]byte, code MailboxIngressDiagnosticCode) (MailboxIngressDiagnosticRecord, MailboxIngressDiagnosticDisposition, error) {
	return s.recordMailboxIngressDiagnosticInMailbox(ctx, ref, requestSHA256, code, nil)
}

// RecordMailboxIngressDiagnosticWithSchemaDetailInMailbox freezes an optional
// validated correction hint with an invalid-request-schema diagnostic. The
// hint is static protocol metadata only, so recovery can reproduce it without
// retaining or rereading the rejected request body.
func (s *AuthorityStore) RecordMailboxIngressDiagnosticWithSchemaDetailInMailbox(ctx context.Context, ref MailboxIngressDiagnosticRef, requestSHA256 [sha256.Size]byte, code MailboxIngressDiagnosticCode, detail *MailboxIngressSchemaDetail) (MailboxIngressDiagnosticRecord, MailboxIngressDiagnosticDisposition, error) {
	return s.recordMailboxIngressDiagnosticInMailbox(ctx, ref, requestSHA256, code, detail)
}

func (s *AuthorityStore) recordMailboxIngressDiagnosticInMailbox(ctx context.Context, ref MailboxIngressDiagnosticRef, requestSHA256 [sha256.Size]byte, code MailboxIngressDiagnosticCode, detail *MailboxIngressSchemaDetail) (MailboxIngressDiagnosticRecord, MailboxIngressDiagnosticDisposition, error) {
	validated, err := validateMailboxIngressDiagnosticRef(ref)
	if err != nil {
		return MailboxIngressDiagnosticRecord{}, "", err
	}
	if !isInitialMailboxIngressDiagnosticCode(code) || !validMailboxIngressSchemaDetail(code, detail) {
		return MailboxIngressDiagnosticRecord{}, "", fmt.Errorf("%w: diagnostic code", ErrMailboxIngressDiagnosticInvalid)
	}
	now := s.now().UTC()
	type result struct {
		record      MailboxIngressDiagnosticRecord
		disposition MailboxIngressDiagnosticDisposition
	}
	stored, err := withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) (result, error) {
		if err := rejectMailboxIngressDiagnosticExchangeCollision(ctx, connection, validated); err != nil {
			return result{}, err
		}
		existing, found, err := readMailboxIngressDiagnosticOnConnection(ctx, connection, validated)
		if err != nil {
			return result{}, err
		}
		if found {
			if existing.RequestSHA256 != requestSHA256 || existing.InputPairRemovedAt != nil {
				return result{record: existing, disposition: MailboxIngressDiagnosticRequestIDReused}, nil
			}
			return result{record: existing, disposition: MailboxIngressDiagnosticSameFingerprint}, nil
		}
		record, err := newMailboxIngressDiagnosticRecord(validated, requestSHA256, code, detail, now)
		if err != nil {
			return result{}, err
		}
		if err := insertMailboxIngressDiagnosticOnConnection(ctx, connection, record); err != nil {
			return result{}, err
		}
		return result{record: record, disposition: MailboxIngressDiagnosticCreated}, nil
	})
	if err != nil {
		return MailboxIngressDiagnosticRecord{}, "", err
	}
	return stored.record, stored.disposition, nil
}

// GetMailboxIngressDiagnosticInMailbox reads the frozen record for recovery
// and private projection. It does not consult normal mailbox exchanges.
func (s *AuthorityStore) GetMailboxIngressDiagnosticInMailbox(ctx context.Context, ref MailboxIngressDiagnosticRef) (MailboxIngressDiagnosticRecord, error) {
	validated, err := validateMailboxIngressDiagnosticRef(ref)
	if err != nil {
		return MailboxIngressDiagnosticRecord{}, err
	}
	return withReadTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) (MailboxIngressDiagnosticRecord, error) {
		record, found, err := readMailboxIngressDiagnosticOnConnection(ctx, connection, validated)
		if err != nil {
			return MailboxIngressDiagnosticRecord{}, err
		}
		if !found {
			return MailboxIngressDiagnosticRecord{}, ErrMailboxIngressDiagnosticNotFound
		}
		return record, nil
	})
}

// MarkMailboxIngressDiagnosticProjectedInMailbox records successful atomic
// projection. A claimed or expired diagnostic must never be re-created.
func (s *AuthorityStore) MarkMailboxIngressDiagnosticProjectedInMailbox(ctx context.Context, ref MailboxIngressDiagnosticRef) (MailboxIngressDiagnosticRecord, error) {
	validated, err := validateMailboxIngressDiagnosticRef(ref)
	if err != nil {
		return MailboxIngressDiagnosticRecord{}, err
	}
	now := s.now().UTC()
	return withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) (MailboxIngressDiagnosticRecord, error) {
		record, found, err := readMailboxIngressDiagnosticOnConnection(ctx, connection, validated)
		if err != nil {
			return MailboxIngressDiagnosticRecord{}, err
		}
		if !found {
			return MailboxIngressDiagnosticRecord{}, ErrMailboxIngressDiagnosticNotFound
		}
		if record.DiagnosticCleanupStartedAt != nil || record.DiagnosticFileRemovedAt != nil || !now.Before(record.DiagnosticCleanupAt) {
			return MailboxIngressDiagnosticRecord{}, ErrMailboxIngressDiagnosticExpired
		}
		if record.ProjectedAt != nil {
			return record, nil
		}
		if _, err := connection.ExecContext(ctx, `
UPDATE mailbox_ingress_diagnostics
SET projected_at = ?
WHERE mailbox_id = ? AND client_request_id = ? AND projected_at IS NULL
`, formatStoredTime(now), validated.MailboxID, validated.ClientRequestID); err != nil {
			return MailboxIngressDiagnosticRecord{}, fmt.Errorf("mark mailbox ingress diagnostic projected: %w", err)
		}
		updated, _, err := readMailboxIngressDiagnosticOnConnection(ctx, connection, validated)
		return updated, err
	})
}

// BeginMailboxIngressDiagnosticInputCleanupInMailbox durably records the
// marker-first cleanup boundary before either input file is unlinked. Recovery
// can then safely recognize an unmarked remaining JSON file after a crash.
func (s *AuthorityStore) BeginMailboxIngressDiagnosticInputCleanupInMailbox(ctx context.Context, ref MailboxIngressDiagnosticRef, requestSHA256 [sha256.Size]byte) (MailboxIngressDiagnosticRecord, error) {
	return s.advanceMailboxIngressDiagnosticInputCleanup(ctx, ref, requestSHA256, false)
}

// MarkMailboxIngressDiagnosticInputPairRemovedInMailbox records completion
// only after the safe marker and JSON pair has been removed. Thereafter even a
// same-fingerprint republish is retained-ID reuse, never executable work.
func (s *AuthorityStore) MarkMailboxIngressDiagnosticInputPairRemovedInMailbox(ctx context.Context, ref MailboxIngressDiagnosticRef, requestSHA256 [sha256.Size]byte) (MailboxIngressDiagnosticRecord, error) {
	return s.advanceMailboxIngressDiagnosticInputCleanup(ctx, ref, requestSHA256, true)
}

func (s *AuthorityStore) advanceMailboxIngressDiagnosticInputCleanup(ctx context.Context, ref MailboxIngressDiagnosticRef, requestSHA256 [sha256.Size]byte, complete bool) (MailboxIngressDiagnosticRecord, error) {
	validated, err := validateMailboxIngressDiagnosticRef(ref)
	if err != nil {
		return MailboxIngressDiagnosticRecord{}, err
	}
	now := s.now().UTC()
	return withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) (MailboxIngressDiagnosticRecord, error) {
		record, found, err := readMailboxIngressDiagnosticOnConnection(ctx, connection, validated)
		if err != nil {
			return MailboxIngressDiagnosticRecord{}, err
		}
		if !found {
			return MailboxIngressDiagnosticRecord{}, ErrMailboxIngressDiagnosticNotFound
		}
		if record.RequestSHA256 != requestSHA256 {
			return MailboxIngressDiagnosticRecord{}, ErrMailboxIngressDiagnosticConflict
		}
		if !now.Before(record.DiagnosticCleanupAt) {
			return MailboxIngressDiagnosticRecord{}, ErrMailboxIngressDiagnosticExpired
		}
		if record.DiagnosticCleanupStartedAt != nil || record.DiagnosticFileRemovedAt != nil {
			return MailboxIngressDiagnosticRecord{}, ErrMailboxIngressDiagnosticCleanupState
		}
		// The diagnostic must be atomically visible before intake removes either
		// member of the safe input pair. This preserves a durable recovery path:
		// after a process stop, the next importer can rebuild the frozen artifact
		// before it revalidates and removes the pair.
		if record.InputPairRemovedAt != nil {
			return record, nil
		}
		if record.ProjectedAt == nil || (complete && record.InputCleanupStartedAt == nil) {
			return MailboxIngressDiagnosticRecord{}, ErrMailboxIngressDiagnosticCleanupState
		}
		if complete {
			if _, err := connection.ExecContext(ctx, `
UPDATE mailbox_ingress_diagnostics
SET input_pair_removed_at = ?
WHERE mailbox_id = ? AND client_request_id = ? AND input_pair_removed_at IS NULL
`, formatStoredTime(now), validated.MailboxID, validated.ClientRequestID); err != nil {
				return MailboxIngressDiagnosticRecord{}, fmt.Errorf("mark mailbox ingress input pair removed: %w", err)
			}
		} else if record.InputCleanupStartedAt == nil {
			if _, err := connection.ExecContext(ctx, `
UPDATE mailbox_ingress_diagnostics
SET input_cleanup_started_at = ?
WHERE mailbox_id = ? AND client_request_id = ? AND input_cleanup_started_at IS NULL
`, formatStoredTime(now), validated.MailboxID, validated.ClientRequestID); err != nil {
				return MailboxIngressDiagnosticRecord{}, fmt.Errorf("begin mailbox ingress input cleanup: %w", err)
			}
		}
		updated, _, err := readMailboxIngressDiagnosticOnConnection(ctx, connection, validated)
		return updated, err
	})
}

// ListRecoverableMailboxIngressDiagnosticsInMailbox returns bounded, active
// diagnostic records for recovery. P165 uses it both to finish incomplete
// input cleanup and to detect a missing projected artifact after a normal
// pair removal. Incomplete records are ordered first. Claimed, removed, and
// expired artifacts are excluded so they cannot be recreated.
func (s *AuthorityStore) ListRecoverableMailboxIngressDiagnosticsInMailbox(ctx context.Context, mailboxID string, limit int) ([]MailboxIngressDiagnosticRecord, error) {
	if err := validateMailboxID(mailboxID); err != nil {
		return nil, ErrMailboxIngressDiagnosticInvalid
	}
	if limit == 0 {
		limit = defaultMailboxIngressDiagnosticRecoveryLimit
	}
	if limit < 1 || limit > 1024 {
		return nil, ErrMailboxIngressDiagnosticInvalid
	}
	now := s.now().UTC()
	return withReadTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) ([]MailboxIngressDiagnosticRecord, error) {
		rows, err := connection.QueryContext(ctx, `
SELECT client_request_id
FROM mailbox_ingress_diagnostics
WHERE mailbox_id = ?
  AND diagnostic_cleanup_started_at IS NULL
  AND diagnostic_file_removed_at IS NULL
  AND diagnostic_cleanup_at > ?
ORDER BY CASE WHEN projected_at IS NULL OR input_pair_removed_at IS NULL THEN 0 ELSE 1 END,
         observed_at, client_request_id
LIMIT ?
`, mailboxID, formatStoredTime(now), limit)
		if err != nil {
			return nil, fmt.Errorf("list recoverable mailbox ingress diagnostics: %w", err)
		}
		defer rows.Close()
		refs := make([]MailboxIngressDiagnosticRef, 0)
		for rows.Next() {
			var requestID string
			if err := rows.Scan(&requestID); err != nil {
				return nil, fmt.Errorf("scan recoverable mailbox ingress diagnostic: %w", err)
			}
			refs = append(refs, MailboxIngressDiagnosticRef{MailboxID: mailboxID, ClientRequestID: requestID})
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate recoverable mailbox ingress diagnostics: %w", err)
		}
		records := make([]MailboxIngressDiagnosticRecord, 0, len(refs))
		for _, ref := range refs {
			record, found, err := readMailboxIngressDiagnosticOnConnection(ctx, connection, ref)
			if err != nil {
				return nil, err
			}
			if found {
				records = append(records, record)
			}
		}
		return records, nil
	})
}

// ClaimMailboxIngressDiagnosticsForCleanupInMailbox atomically claims the
// seven-day private artifacts. The caller may remove a missing file after a
// crash, then mark it removed; a claim prevents later recovery projection.
func (s *AuthorityStore) ClaimMailboxIngressDiagnosticsForCleanupInMailbox(ctx context.Context, mailboxID string, limit int) ([]MailboxIngressDiagnosticRecord, error) {
	if err := validateMailboxID(mailboxID); err != nil {
		return nil, ErrMailboxIngressDiagnosticInvalid
	}
	if limit == 0 {
		limit = defaultMailboxIngressDiagnosticRecoveryLimit
	}
	if limit < 1 || limit > 1024 {
		return nil, ErrMailboxIngressDiagnosticInvalid
	}
	now := s.now().UTC()
	return withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) ([]MailboxIngressDiagnosticRecord, error) {
		rows, err := connection.QueryContext(ctx, `
SELECT client_request_id
FROM mailbox_ingress_diagnostics
WHERE mailbox_id = ?
  AND input_pair_removed_at IS NOT NULL
  AND diagnostic_cleanup_at <= ?
  AND diagnostic_file_removed_at IS NULL
ORDER BY diagnostic_cleanup_at, client_request_id
LIMIT ?
`, mailboxID, formatStoredTime(now), limit)
		if err != nil {
			return nil, fmt.Errorf("select mailbox ingress diagnostics for cleanup: %w", err)
		}
		refs := make([]MailboxIngressDiagnosticRef, 0)
		for rows.Next() {
			var requestID string
			if err := rows.Scan(&requestID); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan mailbox ingress diagnostic cleanup: %w", err)
			}
			refs = append(refs, MailboxIngressDiagnosticRef{MailboxID: mailboxID, ClientRequestID: requestID})
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("iterate mailbox ingress diagnostic cleanup: %w", err)
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("close mailbox ingress diagnostic cleanup rows: %w", err)
		}
		records := make([]MailboxIngressDiagnosticRecord, 0, len(refs))
		for _, ref := range refs {
			if _, err := connection.ExecContext(ctx, `
UPDATE mailbox_ingress_diagnostics
SET diagnostic_cleanup_started_at = COALESCE(diagnostic_cleanup_started_at, ?)
WHERE mailbox_id = ? AND client_request_id = ?
  AND diagnostic_file_removed_at IS NULL
	`, formatStoredTime(now), ref.MailboxID, ref.ClientRequestID); err != nil {
				return nil, fmt.Errorf("claim mailbox ingress diagnostic cleanup: %w", err)
			}
			record, found, err := readMailboxIngressDiagnosticOnConnection(ctx, connection, ref)
			if err != nil {
				return nil, err
			}
			if found {
				records = append(records, record)
			}
		}
		return records, nil
	})
}

// MarkMailboxIngressDiagnosticFileRemovedInMailbox completes a claimed
// diagnostic cleanup. It is idempotent after a crash following the unlink.
func (s *AuthorityStore) MarkMailboxIngressDiagnosticFileRemovedInMailbox(ctx context.Context, ref MailboxIngressDiagnosticRef) (MailboxIngressDiagnosticRecord, error) {
	validated, err := validateMailboxIngressDiagnosticRef(ref)
	if err != nil {
		return MailboxIngressDiagnosticRecord{}, err
	}
	now := s.now().UTC()
	return withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) (MailboxIngressDiagnosticRecord, error) {
		record, found, err := readMailboxIngressDiagnosticOnConnection(ctx, connection, validated)
		if err != nil {
			return MailboxIngressDiagnosticRecord{}, err
		}
		if !found {
			return MailboxIngressDiagnosticRecord{}, ErrMailboxIngressDiagnosticNotFound
		}
		if record.DiagnosticCleanupStartedAt == nil {
			return MailboxIngressDiagnosticRecord{}, ErrMailboxIngressDiagnosticCleanupState
		}
		if record.DiagnosticFileRemovedAt == nil {
			if _, err := connection.ExecContext(ctx, `
UPDATE mailbox_ingress_diagnostics
SET diagnostic_file_removed_at = ?
WHERE mailbox_id = ? AND client_request_id = ? AND diagnostic_file_removed_at IS NULL
`, formatStoredTime(now), validated.MailboxID, validated.ClientRequestID); err != nil {
				return MailboxIngressDiagnosticRecord{}, fmt.Errorf("mark mailbox ingress diagnostic file removed: %w", err)
			}
		}
		updated, _, err := readMailboxIngressDiagnosticOnConnection(ctx, connection, validated)
		return updated, err
	})
}

func newMailboxIngressDiagnosticRecord(ref MailboxIngressDiagnosticRef, requestSHA256 [sha256.Size]byte, code MailboxIngressDiagnosticCode, detail *MailboxIngressSchemaDetail, observedAt time.Time) (MailboxIngressDiagnosticRecord, error) {
	message, ok := mailboxIngressDiagnosticMessage(code)
	if !ok || !isInitialMailboxIngressDiagnosticCode(code) || !validMailboxIngressSchemaDetail(code, detail) {
		return MailboxIngressDiagnosticRecord{}, ErrMailboxIngressDiagnosticInvalid
	}
	record := MailboxIngressDiagnosticRecord{
		MailboxID: ref.MailboxID, RequestID: ref.ClientRequestID, RequestSHA256: requestSHA256,
		Code: code, DiagnosticRevision: 1, ObservedAt: observedAt.UTC(),
		DiagnosticCleanupAt: observedAt.UTC().Add(MailboxIngressDiagnosticArtifactLifetime),
	}
	wire := mailboxIngressDiagnosticWire{
		InboxID: record.MailboxID, RequestID: record.RequestID,
		DiagnosticRevision: record.DiagnosticRevision, LifecyclePhase: "ingress_validation",
		Accepted: false, Executed: false, Code: record.Code, Message: message,
		ObservedAt: formatStoredTime(record.ObservedAt),
	}
	if detail != nil {
		wire.SchemaDetail = mailboxIngressSchemaDetailWireFrom(detail)
	}
	encoded, err := json.Marshal(wire)
	if err != nil || len(encoded) > domain.MaxSerializedRequestBytes {
		return MailboxIngressDiagnosticRecord{}, ErrMailboxIngressDiagnosticInvalid
	}
	record.DiagnosticBytes = encoded
	record.DiagnosticSHA256 = sha256.Sum256(encoded)
	return record, nil
}

func mailboxIngressSchemaDetailWireFrom(detail *MailboxIngressSchemaDetail) *mailboxIngressSchemaDetailWire {
	if detail == nil {
		return nil
	}
	wire := &mailboxIngressSchemaDetailWire{
		SchemaVersion: detail.SchemaVersion, JSONPointer: detail.JSONPointer, Expected: detail.Expected,
		ReceivedType: detail.ReceivedType, CanonicalRunField: detail.CanonicalRunField, CanonicalRunType: detail.CanonicalRunType,
	}
	wire.MinimalValidRun.RequestID = detail.MinimalRunRequestID
	wire.MinimalValidRun.IdempotencyKey = detail.MinimalRunKey
	wire.MinimalValidRun.Operation = detail.MinimalRunOperation
	wire.MinimalValidRun.Script = detail.MinimalRunScript
	return wire
}

func validMailboxIngressSchemaDetail(code MailboxIngressDiagnosticCode, detail *MailboxIngressSchemaDetail) bool {
	if detail == nil {
		return true
	}
	if code != MailboxIngressDiagnosticInvalidRequestSchema || detail.SchemaVersion != "v1" ||
		detail.CanonicalRunField != "script" || detail.CanonicalRunType != "string" ||
		detail.MinimalRunRequestID != "<new-request-id>" || detail.MinimalRunKey != "<new-idempotency-key>" ||
		detail.MinimalRunOperation != "run" || detail.MinimalRunScript != "<shell script>" {
		return false
	}
	switch detail.JSONPointer {
	case "/script":
		if detail.Expected != "required string" && detail.Expected != "string" {
			return false
		}
	case "/command", "/argv", "/cwd":
		if detail.Expected != "unsupported field; use script" {
			return false
		}
	case "":
		if detail.Expected != "documented v1 run fields" {
			return false
		}
	default:
		return false
	}
	switch detail.ReceivedType {
	case "missing", "null", "boolean", "number", "string", "array", "object":
		return true
	default:
		return false
	}
}

func isMailboxIngressDiagnosticCode(code MailboxIngressDiagnosticCode) bool {
	_, ok := mailboxIngressDiagnosticMessage(code)
	return ok
}

func isInitialMailboxIngressDiagnosticCode(code MailboxIngressDiagnosticCode) bool {
	return code != MailboxIngressDiagnosticRequestIDReusedAfterReject && isMailboxIngressDiagnosticCode(code)
}

func mailboxIngressDiagnosticMessage(code MailboxIngressDiagnosticCode) (string, bool) {
	switch code {
	case MailboxIngressDiagnosticMalformedJSON:
		return "request is not valid JSON", true
	case MailboxIngressDiagnosticInvalidRequestSchema:
		return "request does not satisfy the mailbox request format", true
	case MailboxIngressDiagnosticRequestIdentityMismatch:
		return "request ID does not match the marker filename", true
	case MailboxIngressDiagnosticInvalidScript:
		return "request script is invalid", true
	case MailboxIngressDiagnosticRequestTooLarge:
		return "request exceeds the mailbox size limit", true
	case MailboxIngressDiagnosticRequestIDReusedAfterReject:
		return "request ID is retained for a previous rejected request", true
	default:
		return "", false
	}
}

func insertMailboxIngressDiagnosticOnConnection(ctx context.Context, connection *sql.Conn, record MailboxIngressDiagnosticRecord) error {
	if err := validateMailboxIngressDiagnosticRecord(record); err != nil {
		return err
	}
	_, err := connection.ExecContext(ctx, `
INSERT INTO mailbox_ingress_diagnostics (
    mailbox_id, client_request_id, request_sha256, diagnostic_code,
    diagnostic_revision, diagnostic_bytes, diagnostic_sha256, observed_at,
    projected_at, input_cleanup_started_at, input_pair_removed_at,
    diagnostic_cleanup_at, diagnostic_cleanup_started_at,
    diagnostic_file_removed_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, NULL, ?, NULL, NULL)
`, record.MailboxID, record.RequestID, record.RequestSHA256[:], string(record.Code),
		record.DiagnosticRevision, record.DiagnosticBytes, record.DiagnosticSHA256[:],
		formatStoredTime(record.ObservedAt), formatStoredTime(record.DiagnosticCleanupAt))
	if err != nil {
		return fmt.Errorf("insert mailbox ingress diagnostic: %w", err)
	}
	return nil
}

func rejectMailboxIngressDiagnosticExchangeCollision(ctx context.Context, connection *sql.Conn, ref MailboxIngressDiagnosticRef) error {
	var exists int
	if err := connection.QueryRowContext(ctx, `
SELECT EXISTS(
    SELECT 1 FROM mailbox_exchanges
    WHERE mailbox_id = ? AND client_request_id = ?
)
`, ref.MailboxID, ref.ClientRequestID).Scan(&exists); err != nil {
		return fmt.Errorf("check mailbox ingress diagnostic exchange collision: %w", err)
	}
	if exists != 0 {
		return ErrMailboxIngressDiagnosticConflict
	}
	return nil
}

// rejectMailboxExchangeIngressDiagnosticCollision reserves a request ID that
// was safely rejected before it can become an accepted mailbox exchange. The
// migration trigger remains the final database boundary; this check gives all
// store acceptance APIs their stable, caller-actionable sentinel instead of
// a driver-specific trigger error.
func rejectMailboxExchangeIngressDiagnosticCollision(ctx context.Context, connection *sql.Conn, ref MailboxExchangeRef) error {
	var exists int
	if err := connection.QueryRowContext(ctx, `
SELECT EXISTS(
    SELECT 1 FROM mailbox_ingress_diagnostics
    WHERE mailbox_id = ? AND client_request_id = ?
)
`, ref.MailboxID, ref.ClientRequestID).Scan(&exists); err != nil {
		return fmt.Errorf("check mailbox exchange ingress diagnostic collision: %w", err)
	}
	if exists != 0 {
		return ErrMailboxIngressDiagnosticConflict
	}
	return nil
}

func readMailboxIngressDiagnosticOnConnection(ctx context.Context, connection *sql.Conn, ref MailboxIngressDiagnosticRef) (MailboxIngressDiagnosticRecord, bool, error) {
	validated, err := validateMailboxIngressDiagnosticRef(ref)
	if err != nil {
		return MailboxIngressDiagnosticRecord{}, false, err
	}
	var record MailboxIngressDiagnosticRecord
	var fingerprint, checksum []byte
	var code, observedAt, cleanupAt string
	var projectedAt, cleanupStartedAt, pairRemovedAt, diagnosticCleanupStartedAt, fileRemovedAt sql.NullString
	err = connection.QueryRowContext(ctx, `
SELECT mailbox_id, client_request_id, request_sha256, diagnostic_code,
       diagnostic_revision, diagnostic_bytes, diagnostic_sha256, observed_at,
       projected_at, input_cleanup_started_at, input_pair_removed_at,
       diagnostic_cleanup_at, diagnostic_cleanup_started_at,
       diagnostic_file_removed_at
FROM mailbox_ingress_diagnostics
WHERE mailbox_id = ? AND client_request_id = ?
`, validated.MailboxID, validated.ClientRequestID).Scan(
		&record.MailboxID, &record.RequestID, &fingerprint, &code,
		&record.DiagnosticRevision, &record.DiagnosticBytes, &checksum, &observedAt,
		&projectedAt, &cleanupStartedAt, &pairRemovedAt, &cleanupAt,
		&diagnosticCleanupStartedAt, &fileRemovedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return MailboxIngressDiagnosticRecord{}, false, nil
	}
	if err != nil {
		return MailboxIngressDiagnosticRecord{}, false, fmt.Errorf("read mailbox ingress diagnostic: %w", err)
	}
	if record.MailboxID != validated.MailboxID || record.RequestID != validated.ClientRequestID || len(fingerprint) != sha256.Size || len(checksum) != sha256.Size {
		return MailboxIngressDiagnosticRecord{}, false, ErrMailboxIngressDiagnosticInvalid
	}
	copy(record.RequestSHA256[:], fingerprint)
	copy(record.DiagnosticSHA256[:], checksum)
	record.Code = MailboxIngressDiagnosticCode(code)
	if record.ObservedAt, err = parseStoredTime(observedAt); err != nil {
		return MailboxIngressDiagnosticRecord{}, false, fmt.Errorf("%w: observed_at", ErrMailboxIngressDiagnosticInvalid)
	}
	if record.DiagnosticCleanupAt, err = parseStoredTime(cleanupAt); err != nil {
		return MailboxIngressDiagnosticRecord{}, false, fmt.Errorf("%w: diagnostic_cleanup_at", ErrMailboxIngressDiagnosticInvalid)
	}
	for _, value := range []struct {
		source sql.NullString
		dest   **time.Time
	}{
		{projectedAt, &record.ProjectedAt},
		{cleanupStartedAt, &record.InputCleanupStartedAt},
		{pairRemovedAt, &record.InputPairRemovedAt},
		{diagnosticCleanupStartedAt, &record.DiagnosticCleanupStartedAt},
		{fileRemovedAt, &record.DiagnosticFileRemovedAt},
	} {
		if !value.source.Valid {
			continue
		}
		parsed, parseErr := parseStoredTime(value.source.String)
		if parseErr != nil {
			return MailboxIngressDiagnosticRecord{}, false, ErrMailboxIngressDiagnosticInvalid
		}
		*value.dest = &parsed
	}
	if err := validateMailboxIngressDiagnosticRecord(record); err != nil {
		return MailboxIngressDiagnosticRecord{}, false, err
	}
	return cloneMailboxIngressDiagnosticRecord(record), true, nil
}

func validateMailboxIngressDiagnosticRecord(record MailboxIngressDiagnosticRecord) error {
	if _, err := NewMailboxIngressDiagnosticRef(record.MailboxID, record.RequestID); err != nil || !isInitialMailboxIngressDiagnosticCode(record.Code) || record.DiagnosticRevision != 1 || len(record.DiagnosticBytes) < 2 || len(record.DiagnosticBytes) > domain.MaxSerializedRequestBytes || record.ObservedAt.IsZero() || record.DiagnosticCleanupAt.IsZero() || !record.DiagnosticCleanupAt.Equal(record.ObservedAt.Add(MailboxIngressDiagnosticArtifactLifetime)) {
		return ErrMailboxIngressDiagnosticInvalid
	}
	if err := validateMailboxIngressDiagnosticLifecycle(record); err != nil {
		return err
	}
	actual := sha256.Sum256(record.DiagnosticBytes)
	if actual != record.DiagnosticSHA256 {
		return ErrMailboxIngressDiagnosticInvalid
	}
	message, ok := mailboxIngressDiagnosticMessage(record.Code)
	if !ok {
		return ErrMailboxIngressDiagnosticInvalid
	}
	var frozen mailboxIngressDiagnosticWire
	decoder := json.NewDecoder(bytes.NewReader(record.DiagnosticBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&frozen); err != nil {
		return ErrMailboxIngressDiagnosticInvalid
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return ErrMailboxIngressDiagnosticInvalid
	}
	if frozen.InboxID != record.MailboxID || frozen.RequestID != record.RequestID || frozen.DiagnosticRevision != record.DiagnosticRevision ||
		frozen.LifecyclePhase != "ingress_validation" || frozen.Accepted || frozen.Executed || frozen.Code != record.Code ||
		frozen.Message != message || frozen.ObservedAt != formatStoredTime(record.ObservedAt) {
		return ErrMailboxIngressDiagnosticInvalid
	}
	if frozen.SchemaDetail != nil {
		detail := mailboxIngressSchemaDetailFromWire(frozen.SchemaDetail)
		if !validMailboxIngressSchemaDetail(record.Code, detail) {
			return ErrMailboxIngressDiagnosticInvalid
		}
	}
	expected, err := json.Marshal(mailboxIngressDiagnosticWire{
		InboxID: record.MailboxID, RequestID: record.RequestID,
		DiagnosticRevision: record.DiagnosticRevision, LifecyclePhase: "ingress_validation",
		Accepted: false, Executed: false, Code: record.Code, Message: message,
		SchemaDetail: frozen.SchemaDetail,
		ObservedAt:   formatStoredTime(record.ObservedAt),
	})
	if err != nil || !bytes.Equal(expected, record.DiagnosticBytes) {
		return ErrMailboxIngressDiagnosticInvalid
	}
	return nil
}

func mailboxIngressSchemaDetailFromWire(wire *mailboxIngressSchemaDetailWire) *MailboxIngressSchemaDetail {
	if wire == nil {
		return nil
	}
	return &MailboxIngressSchemaDetail{
		SchemaVersion: wire.SchemaVersion, JSONPointer: wire.JSONPointer, Expected: wire.Expected, ReceivedType: wire.ReceivedType,
		CanonicalRunField: wire.CanonicalRunField, CanonicalRunType: wire.CanonicalRunType,
		MinimalRunRequestID: wire.MinimalValidRun.RequestID, MinimalRunKey: wire.MinimalValidRun.IdempotencyKey,
		MinimalRunOperation: wire.MinimalValidRun.Operation, MinimalRunScript: wire.MinimalValidRun.Script,
	}
}

// validateMailboxIngressDiagnosticLifecycle keeps every persisted phase in
// its only safe order. SQLite prevents stage skipping and mutation; this
// validation also rejects a malformed or manually corrupted timestamp order
// when a record is read back through the store.
func validateMailboxIngressDiagnosticLifecycle(record MailboxIngressDiagnosticRecord) error {
	if record.ProjectedAt != nil {
		if record.ProjectedAt.IsZero() || record.ProjectedAt.Before(record.ObservedAt) || !record.ProjectedAt.Before(record.DiagnosticCleanupAt) {
			return ErrMailboxIngressDiagnosticInvalid
		}
	}
	if record.InputCleanupStartedAt != nil {
		if record.InputCleanupStartedAt.IsZero() || record.ProjectedAt == nil || record.InputCleanupStartedAt.Before(*record.ProjectedAt) || !record.InputCleanupStartedAt.Before(record.DiagnosticCleanupAt) {
			return ErrMailboxIngressDiagnosticInvalid
		}
	}
	if record.InputPairRemovedAt != nil {
		if record.InputPairRemovedAt.IsZero() || record.InputCleanupStartedAt == nil || record.InputPairRemovedAt.Before(*record.InputCleanupStartedAt) || !record.InputPairRemovedAt.Before(record.DiagnosticCleanupAt) {
			return ErrMailboxIngressDiagnosticInvalid
		}
	}
	if record.DiagnosticCleanupStartedAt != nil {
		if record.DiagnosticCleanupStartedAt.IsZero() || record.InputPairRemovedAt == nil || record.DiagnosticCleanupStartedAt.Before(*record.InputPairRemovedAt) || record.DiagnosticCleanupStartedAt.Before(record.DiagnosticCleanupAt) {
			return ErrMailboxIngressDiagnosticInvalid
		}
	}
	if record.DiagnosticFileRemovedAt != nil {
		if record.DiagnosticFileRemovedAt.IsZero() || record.DiagnosticCleanupStartedAt == nil || record.DiagnosticFileRemovedAt.Before(*record.DiagnosticCleanupStartedAt) {
			return ErrMailboxIngressDiagnosticInvalid
		}
	}
	return nil
}

func cloneMailboxIngressDiagnosticRecord(record MailboxIngressDiagnosticRecord) MailboxIngressDiagnosticRecord {
	record.DiagnosticBytes = append([]byte(nil), record.DiagnosticBytes...)
	record.ProjectedAt = cloneMailboxIngressDiagnosticTime(record.ProjectedAt)
	record.InputCleanupStartedAt = cloneMailboxIngressDiagnosticTime(record.InputCleanupStartedAt)
	record.InputPairRemovedAt = cloneMailboxIngressDiagnosticTime(record.InputPairRemovedAt)
	record.DiagnosticCleanupStartedAt = cloneMailboxIngressDiagnosticTime(record.DiagnosticCleanupStartedAt)
	record.DiagnosticFileRemovedAt = cloneMailboxIngressDiagnosticTime(record.DiagnosticFileRemovedAt)
	return record
}

func cloneMailboxIngressDiagnosticTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
