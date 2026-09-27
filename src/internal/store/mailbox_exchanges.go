package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"remote-session-runner/src/internal/domain"
)

var (
	ErrMailboxExchangeInvalid   = errors.New("invalid mailbox exchange")
	ErrMailboxExchangeConflict  = errors.New("mailbox request ID or idempotency conflict")
	ErrMailboxExchangeNotFound  = errors.New("mailbox exchange not found")
	ErrMailboxResponseInvalid   = errors.New("invalid mailbox response")
	ErrMailboxTerminalImmutable = errors.New("terminal mailbox response is immutable")
)

// MailboxExchangeState is the durable file-exchange lifecycle. Terminal
// response revisions are added by later mailbox phases; P082 persists the
// receipt and accepted/terminal state before any file cleanup.
type MailboxExchangeState string

const (
	MailboxExchangeAccepted      MailboxExchangeState = "accepted"
	MailboxExchangeComplete      MailboxExchangeState = "complete"
	MailboxExchangeRejected      MailboxExchangeState = "rejected"
	MailboxExchangeIndeterminate MailboxExchangeState = "indeterminate"
)

// MailboxExchangeCreate contains the immutable receipt binding. CanonicalPayload
// must exclude request_id and idempotency_key for mutation retries.
type MailboxExchangeCreate struct {
	RequestID        string
	Operation        string
	Controller       domain.ControllerIdentity
	IdempotencyKey   string
	RequestHash      domain.CanonicalHash
	CanonicalPayload []byte
	ResourceID       string
}

// MailboxExchangeRecord is the durable request receipt and its current state.
// Response bytes/revisions are intentionally reserved for later phases.
type MailboxExchangeRecord struct {
	RequestID              string
	Operation              string
	Controller             domain.ControllerIdentity
	IdempotencyKey         string
	RequestHash            domain.CanonicalHash
	CanonicalPayload       []byte
	ResourceID             string
	State                  MailboxExchangeState
	ResponseRevision       int64
	ResponseBytes          []byte
	ResponseSHA256         []byte
	TerminalResponseBytes  []byte
	TerminalResponseSHA256 []byte
	AvailableEventSequence *int64
	AcknowledgedAt         *time.Time
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

// MailboxResponsePublication is one response snapshot. Accepted snapshots
// may be replaced by a higher revision; terminal snapshots become immutable.
type MailboxResponsePublication struct {
	State                  MailboxExchangeState
	Bytes                  []byte
	AvailableEventSequence *int64
}

// AcceptMailboxExchange binds one request_id in the same SQLite transaction
// used to detect a retained same-key retry. Reusing a request_id with changed
// semantics conflicts. A new request_id with the same operation/controller/
// idempotency key and hash receives the original binding and state without
// invoking a mutation a second time.
func (s *AuthorityStore) AcceptMailboxExchange(ctx context.Context, input MailboxExchangeCreate) (record MailboxExchangeRecord, duplicate bool, err error) {
	validated, err := validateMailboxExchangeCreate(input)
	if err != nil {
		return MailboxExchangeRecord{}, false, err
	}
	now := s.now().UTC()
	returnValue, err := withImmediateTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) (MailboxExchangeRecord, error) {
		byID, found, err := readMailboxExchangeByIDOnConnection(ctx, connection, validated.RequestID)
		if err != nil {
			return MailboxExchangeRecord{}, err
		}
		if found {
			if !sameMailboxBinding(byID, validated) {
				return MailboxExchangeRecord{}, ErrMailboxExchangeConflict
			}
			duplicate = true
			return byID, nil
		}
		if validated.IdempotencyKey != "" {
			existing, found, err := readLatestMailboxExchangeByKeyOnConnection(ctx, connection, validated.Controller, validated.Operation, validated.IdempotencyKey)
			if err != nil {
				return MailboxExchangeRecord{}, err
			}
			if found {
				if domain.CompareIdempotency(existing.RequestHash, validated.RequestHash) == domain.IdempotencyConflict {
					return MailboxExchangeRecord{}, ErrIdempotencyConflict
				}
				if !bytesEqual(existing.CanonicalPayload, validated.CanonicalPayload) {
					return MailboxExchangeRecord{}, ErrIdempotencyConflict
				}
				validated.ResourceID = existing.ResourceID
				validated.State = existing.State
				validated.ResponseRevision = existing.ResponseRevision
				validated.ResponseBytes = append([]byte(nil), existing.ResponseBytes...)
				validated.ResponseSHA256 = append([]byte(nil), existing.ResponseSHA256...)
				validated.TerminalResponseBytes = append([]byte(nil), existing.TerminalResponseBytes...)
				validated.TerminalResponseSHA256 = append([]byte(nil), existing.TerminalResponseSHA256...)
				if existing.AvailableEventSequence != nil {
					cursor := *existing.AvailableEventSequence
					validated.AvailableEventSequence = &cursor
				}
				validated.CreatedAt = now
				validated.UpdatedAt = now
				if err := insertMailboxExchangeOnConnection(ctx, connection, validated); err != nil {
					return MailboxExchangeRecord{}, err
				}
				duplicate = true
				return readMailboxExchangeOnConnection(ctx, connection, validated.RequestID)
			}
		}
		validated.State = MailboxExchangeAccepted
		validated.CreatedAt = now
		validated.UpdatedAt = now
		if err := insertMailboxExchangeOnConnection(ctx, connection, validated); err != nil {
			return MailboxExchangeRecord{}, err
		}
		return readMailboxExchangeOnConnection(ctx, connection, validated.RequestID)
	})
	if err != nil {
		return MailboxExchangeRecord{}, false, err
	}
	return returnValue, duplicate, nil
}

// GetMailboxExchange reloads and validates one durable receipt.
func (s *AuthorityStore) GetMailboxExchange(ctx context.Context, requestID string) (MailboxExchangeRecord, error) {
	if err := validateMailboxRequestID(requestID); err != nil {
		return MailboxExchangeRecord{}, err
	}
	return withImmediateTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) (MailboxExchangeRecord, error) {
		return readMailboxExchangeOnConnection(ctx, connection, requestID)
	})
}

// CompleteMailboxExchange advances an accepted receipt to one terminal
// mailbox state. Repeating the same terminal state is idempotent; changing a
// terminal outcome is rejected so a later response phase cannot rewrite it.
func (s *AuthorityStore) CompleteMailboxExchange(ctx context.Context, requestID string, next MailboxExchangeState) (MailboxExchangeRecord, error) {
	if err := validateMailboxRequestID(requestID); err != nil {
		return MailboxExchangeRecord{}, err
	}
	if next != MailboxExchangeComplete && next != MailboxExchangeRejected && next != MailboxExchangeIndeterminate {
		return MailboxExchangeRecord{}, fmt.Errorf("%w: terminal state %q", ErrMailboxExchangeInvalid, next)
	}
	now := s.now().UTC()
	return withImmediateTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) (MailboxExchangeRecord, error) {
		current, err := readMailboxExchangeOnConnection(ctx, connection, requestID)
		if err != nil {
			return MailboxExchangeRecord{}, err
		}
		if current.State != MailboxExchangeAccepted {
			if current.State == next {
				return current, nil
			}
			return MailboxExchangeRecord{}, ErrMailboxExchangeConflict
		}
		if _, err := connection.ExecContext(ctx, `
UPDATE mailbox_exchanges
SET request_state = ?, updated_at = ?
WHERE controller_type = ? AND controller_id = ? AND operation = ?
  AND idempotency_key = ? AND canonical_hash_version = ? AND canonical_hash = ?
`, string(next), formatStoredTime(now), string(current.Controller.Type()), string(current.Controller.ID()), current.Operation, current.IdempotencyKey, current.RequestHash.Version(), current.RequestHash.SHA256()); err != nil {
			return MailboxExchangeRecord{}, fmt.Errorf("complete mailbox exchange: %w", err)
		}
		return readMailboxExchangeOnConnection(ctx, connection, requestID)
	})
}

// PublishMailboxResponse stores one response revision. Nonterminal snapshots
// increment response_revision; a terminal snapshot is copied into immutable
// terminal columns and cannot be changed by a later retry of the same ID.
func (s *AuthorityStore) PublishMailboxResponse(ctx context.Context, requestID string, publication MailboxResponsePublication) (MailboxExchangeRecord, error) {
	if err := validateMailboxRequestID(requestID); err != nil {
		return MailboxExchangeRecord{}, err
	}
	if publication.State != MailboxExchangeAccepted && publication.State != MailboxExchangeComplete && publication.State != MailboxExchangeRejected && publication.State != MailboxExchangeIndeterminate {
		return MailboxExchangeRecord{}, fmt.Errorf("%w: state %q", ErrMailboxResponseInvalid, publication.State)
	}
	if len(publication.Bytes) == 0 || len(publication.Bytes) > domain.MaxSerializedRequestBytes {
		return MailboxExchangeRecord{}, fmt.Errorf("%w: response byte size", ErrMailboxResponseInvalid)
	}
	if publication.AvailableEventSequence != nil && *publication.AvailableEventSequence < 0 {
		return MailboxExchangeRecord{}, fmt.Errorf("%w: available event cursor", ErrMailboxResponseInvalid)
	}
	responseHash := sha256Bytes(publication.Bytes)
	now := s.now().UTC()
	return withImmediateTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) (MailboxExchangeRecord, error) {
		current, err := readMailboxExchangeOnConnection(ctx, connection, requestID)
		if err != nil {
			return MailboxExchangeRecord{}, err
		}
		if current.State != MailboxExchangeAccepted {
			if current.ResponseRevision > 0 && bytesEqual(current.ResponseBytes, publication.Bytes) && current.State == publication.State && sameCursor(current.AvailableEventSequence, publication.AvailableEventSequence) {
				return current, nil
			}
			return MailboxExchangeRecord{}, ErrMailboxTerminalImmutable
		}
		nextRevision := current.ResponseRevision + 1
		if nextRevision <= 0 {
			return MailboxExchangeRecord{}, fmt.Errorf("%w: response revision overflow", ErrMailboxResponseInvalid)
		}
		var terminalBytes, terminalHash any
		if publication.State == MailboxExchangeComplete || publication.State == MailboxExchangeRejected || publication.State == MailboxExchangeIndeterminate {
			terminalBytes, terminalHash = publication.Bytes, responseHash
		}
		if _, err := connection.ExecContext(ctx, `
UPDATE mailbox_exchanges
SET request_state = ?, response_revision = ?, response_bytes = ?, response_sha256 = ?,
    terminal_response_bytes = ?, terminal_response_sha256 = ?, available_event_sequence = ?, updated_at = ?
WHERE request_id = ? AND request_state = 'accepted'
`, string(publication.State), nextRevision, publication.Bytes, responseHash, terminalBytes, terminalHash, publication.AvailableEventSequence, formatStoredTime(now), requestID); err != nil {
			return MailboxExchangeRecord{}, fmt.Errorf("publish mailbox response: %w", err)
		}
		return readMailboxExchangeOnConnection(ctx, connection, requestID)
	})
}

func validateMailboxExchangeCreate(input MailboxExchangeCreate) (validatedMailboxExchangeCreate, error) {
	if err := validateMailboxRequestID(input.RequestID); err != nil {
		return validatedMailboxExchangeCreate{}, err
	}
	if input.Operation == "" || len(input.Operation) > 128 || strings.IndexByte(input.Operation, 0) >= 0 {
		return validatedMailboxExchangeCreate{}, fmt.Errorf("%w: operation", ErrMailboxExchangeInvalid)
	}
	controller, err := validateController(input.Controller)
	if err != nil {
		return validatedMailboxExchangeCreate{}, fmt.Errorf("%w: controller: %v", ErrMailboxExchangeInvalid, err)
	}
	if len(input.IdempotencyKey) > 256 || strings.IndexByte(input.IdempotencyKey, 0) >= 0 {
		return validatedMailboxExchangeCreate{}, fmt.Errorf("%w: idempotency key", ErrMailboxExchangeInvalid)
	}
	hash, err := domain.NewCanonicalHash(input.RequestHash.Version(), input.RequestHash.SHA256())
	if err != nil {
		return validatedMailboxExchangeCreate{}, fmt.Errorf("%w: request hash: %v", ErrMailboxExchangeInvalid, err)
	}
	if len(input.CanonicalPayload) == 0 || len(input.CanonicalPayload) > domain.MaxSerializedRequestBytes {
		return validatedMailboxExchangeCreate{}, fmt.Errorf("%w: canonical payload size", ErrMailboxExchangeInvalid)
	}
	if input.ResourceID != "" && (len(input.ResourceID) > 256 || strings.IndexByte(input.ResourceID, 0) >= 0) {
		return validatedMailboxExchangeCreate{}, fmt.Errorf("%w: resource ID", ErrMailboxExchangeInvalid)
	}
	return validatedMailboxExchangeCreate{MailboxExchangeCreate: MailboxExchangeCreate{RequestID: input.RequestID, Operation: input.Operation, Controller: controller, IdempotencyKey: input.IdempotencyKey, RequestHash: hash, CanonicalPayload: append([]byte(nil), input.CanonicalPayload...), ResourceID: input.ResourceID}}, nil
}

type validatedMailboxExchangeCreate struct {
	MailboxExchangeCreate
	State                  MailboxExchangeState
	ResponseRevision       int64
	ResponseBytes          []byte
	ResponseSHA256         []byte
	TerminalResponseBytes  []byte
	TerminalResponseSHA256 []byte
	AvailableEventSequence *int64
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

func validateMailboxRequestID(requestID string) error {
	if requestID == "" || len(requestID) > 256 || strings.IndexByte(requestID, 0) >= 0 {
		return fmt.Errorf("%w: request ID must be 1..256 bytes and contain no NUL", ErrMailboxExchangeInvalid)
	}
	return nil
}

func sameMailboxBinding(existing MailboxExchangeRecord, input validatedMailboxExchangeCreate) bool {
	return existing.Operation == input.Operation && existing.Controller.Type() == input.Controller.Type() && existing.Controller.ID() == input.Controller.ID() && existing.IdempotencyKey == input.IdempotencyKey && domain.CompareIdempotency(existing.RequestHash, input.RequestHash) == domain.IdempotencySamePayload && bytesEqual(existing.CanonicalPayload, input.CanonicalPayload)
}

func insertMailboxExchangeOnConnection(ctx context.Context, connection *sql.Conn, input validatedMailboxExchangeCreate) error {
	_, err := connection.ExecContext(ctx, `
INSERT INTO mailbox_exchanges (
    request_id, operation, controller_type, controller_id, idempotency_key,
    canonical_hash_version, canonical_hash, canonical_payload, resource_id,
    request_state, response_revision, terminal_response_bytes,
    terminal_response_sha256, available_event_sequence, response_bytes,
    response_sha256, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, input.RequestID, input.Operation, string(input.Controller.Type()), string(input.Controller.ID()), input.IdempotencyKey,
		input.RequestHash.Version(), input.RequestHash.SHA256(), input.CanonicalPayload, input.ResourceID, string(input.State), input.ResponseRevision,
		nullableBytes(input.TerminalResponseBytes), nullableBytes(input.TerminalResponseSHA256), input.AvailableEventSequence,
		nullableBytes(input.ResponseBytes), nullableBytes(input.ResponseSHA256), formatStoredTime(input.CreatedAt), formatStoredTime(input.UpdatedAt))
	if err != nil {
		return fmt.Errorf("insert mailbox exchange: %w", err)
	}
	return nil
}

func readMailboxExchangeByIDOnConnection(ctx context.Context, connection *sql.Conn, requestID string) (MailboxExchangeRecord, bool, error) {
	var exists int
	if err := connection.QueryRowContext(ctx, `SELECT count(*) FROM mailbox_exchanges WHERE request_id = ?`, requestID).Scan(&exists); err != nil {
		return MailboxExchangeRecord{}, false, fmt.Errorf("lookup mailbox request ID: %w", err)
	}
	if exists == 0 {
		return MailboxExchangeRecord{}, false, nil
	}
	record, err := readMailboxExchangeOnConnection(ctx, connection, requestID)
	return record, true, err
}

func readLatestMailboxExchangeByKeyOnConnection(ctx context.Context, connection *sql.Conn, controller domain.ControllerIdentity, operation, key string) (MailboxExchangeRecord, bool, error) {
	var requestID string
	err := connection.QueryRowContext(ctx, `
SELECT request_id FROM mailbox_exchanges
WHERE controller_type = ? AND controller_id = ? AND operation = ? AND idempotency_key = ?
ORDER BY created_at DESC, request_id DESC LIMIT 1
`, string(controller.Type()), string(controller.ID()), operation, key).Scan(&requestID)
	if errors.Is(err, sql.ErrNoRows) {
		return MailboxExchangeRecord{}, false, nil
	}
	if err != nil {
		return MailboxExchangeRecord{}, false, fmt.Errorf("lookup mailbox idempotency key: %w", err)
	}
	record, err := readMailboxExchangeOnConnection(ctx, connection, requestID)
	return record, true, err
}

func readMailboxExchangeOnConnection(ctx context.Context, connection *sql.Conn, requestID string) (MailboxExchangeRecord, error) {
	var record MailboxExchangeRecord
	var controllerType, controllerID, operation, key, payload, resourceID, state, createdAt, updatedAt string
	var version int
	var digest []byte
	var terminalBytes, terminalHash, responseBytes, responseHash []byte
	var availableCursor sql.NullInt64
	var acknowledgedAt sql.NullString
	if err := connection.QueryRowContext(ctx, `
SELECT request_id, operation, controller_type, controller_id, idempotency_key,
       canonical_hash_version, canonical_hash, canonical_payload, resource_id,
       request_state, response_revision, terminal_response_bytes,
       terminal_response_sha256, available_event_sequence, response_bytes,
       response_sha256, acknowledged_at, created_at, updated_at
FROM mailbox_exchanges WHERE request_id = ?
`, requestID).Scan(&record.RequestID, &operation, &controllerType, &controllerID, &key, &version, &digest, &payload, &resourceID, &state, &record.ResponseRevision, &terminalBytes, &terminalHash, &availableCursor, &responseBytes, &responseHash, &acknowledgedAt, &createdAt, &updatedAt); errors.Is(err, sql.ErrNoRows) {
		return MailboxExchangeRecord{}, ErrMailboxExchangeNotFound
	} else if err != nil {
		return MailboxExchangeRecord{}, fmt.Errorf("read mailbox exchange: %w", err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerType(controllerType), domain.ControllerID(controllerID))
	if err != nil {
		return MailboxExchangeRecord{}, fmt.Errorf("%w: controller: %v", ErrMailboxExchangeInvalid, err)
	}
	hash, err := domain.NewCanonicalHash(uint16(version), digest)
	if err != nil || len(payload) == 0 {
		return MailboxExchangeRecord{}, fmt.Errorf("%w: request hash or payload", ErrMailboxExchangeInvalid)
	}
	if state != string(MailboxExchangeAccepted) && state != string(MailboxExchangeComplete) && state != string(MailboxExchangeRejected) && state != string(MailboxExchangeIndeterminate) {
		return MailboxExchangeRecord{}, fmt.Errorf("%w: state %q", ErrMailboxExchangeInvalid, state)
	}
	if record.ResponseRevision < 0 {
		return MailboxExchangeRecord{}, fmt.Errorf("%w: response revision", ErrMailboxExchangeInvalid)
	}
	if len(responseBytes) > 0 {
		if len(responseHash) != sha256.Size || !bytesEqual(responseHash, sha256Bytes(responseBytes)) {
			return MailboxExchangeRecord{}, fmt.Errorf("%w: response hash", ErrMailboxResponseInvalid)
		}
		record.ResponseBytes, record.ResponseSHA256 = append([]byte(nil), responseBytes...), append([]byte(nil), responseHash...)
	}
	if len(terminalBytes) > 0 {
		if len(terminalHash) != sha256.Size || !bytesEqual(terminalHash, sha256Bytes(terminalBytes)) {
			return MailboxExchangeRecord{}, fmt.Errorf("%w: terminal response hash", ErrMailboxResponseInvalid)
		}
		record.TerminalResponseBytes, record.TerminalResponseSHA256 = append([]byte(nil), terminalBytes...), append([]byte(nil), terminalHash...)
	}
	if availableCursor.Valid {
		if availableCursor.Int64 < 0 {
			return MailboxExchangeRecord{}, fmt.Errorf("%w: available event cursor", ErrMailboxResponseInvalid)
		}
		cursor := availableCursor.Int64
		record.AvailableEventSequence = &cursor
	}
	if acknowledgedAt.Valid {
		value, err := parseStoredTime(acknowledgedAt.String)
		if err != nil {
			return MailboxExchangeRecord{}, fmt.Errorf("%w: acknowledged_at: %v", ErrMailboxExchangeInvalid, err)
		}
		record.AcknowledgedAt = &value
	}
	if record.CreatedAt, err = parseStoredTime(createdAt); err != nil {
		return MailboxExchangeRecord{}, fmt.Errorf("%w: created_at: %v", ErrMailboxExchangeInvalid, err)
	}
	if record.UpdatedAt, err = parseStoredTime(updatedAt); err != nil {
		return MailboxExchangeRecord{}, fmt.Errorf("%w: updated_at: %v", ErrMailboxExchangeInvalid, err)
	}
	record.Operation, record.Controller, record.IdempotencyKey = operation, controller, key
	record.RequestHash, record.CanonicalPayload, record.ResourceID, record.State = hash, append([]byte(nil), payload...), resourceID, MailboxExchangeState(state)
	return record, nil
}

func bytesEqual(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sha256Bytes(value []byte) []byte {
	digest := sha256.Sum256(value)
	return append([]byte(nil), digest[:]...)
}

func nullableBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

func sameCursor(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
