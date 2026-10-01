package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"remote-session-runner/src/internal/domain"
)

var (
	ErrMailboxExchangeInvalid       = errors.New("invalid mailbox exchange")
	ErrMailboxExchangeConflict      = errors.New("mailbox request ID or idempotency conflict")
	ErrMailboxExchangeNotFound      = errors.New("mailbox exchange not found")
	ErrMailboxResponseInvalid       = errors.New("invalid mailbox response")
	ErrMailboxResponseExpired       = errors.New("terminal mailbox response cleanup deadline has passed")
	ErrMailboxEventReferenceExpired = errors.New("mailbox event file cleanup has started")
	ErrMailboxTerminalImmutable     = errors.New("terminal mailbox response is immutable")
	// ErrMailboxConfigurationPending prevents an inbox removal or relocation
	// from abandoning accepted work, a live response, or a marker-last request
	// that a file-only producer can publish while the service is stopped.
	ErrMailboxConfigurationPending = errors.New("mailbox configuration would abandon retained ingress")
)

const (
	MailboxAckedResponseLifetime   = 24 * time.Hour
	MailboxUnackedResponseLifetime = 7 * 24 * time.Hour

	// MailboxExecutionSelectionInboxDefault identifies a selection supplied by
	// the trusted configured mailbox default.
	MailboxExecutionSelectionInboxDefault = "inbox_default"
	// MailboxExecutionSelectionRequestOverride identifies a complete client
	// selection that passed the mailbox allow-list.
	MailboxExecutionSelectionRequestOverride = "request_override"
)

// MailboxExecutionSelectionState records whether a new-work receipt has
// trusted P153 selection provenance. Legacy marks rows that existed before
// P153 added this state; it must never be used to represent a newly rejected
// P153 request.
type MailboxExecutionSelectionState string

const (
	MailboxExecutionSelectionLegacy   MailboxExecutionSelectionState = "legacy"
	MailboxExecutionSelectionResolved MailboxExecutionSelectionState = "resolved"
	MailboxExecutionSelectionRejected MailboxExecutionSelectionState = "rejected"
)

// MailboxExchangeState is the durable file-exchange lifecycle. P082 persists
// receipts before pair cleanup; response revisions and cleanup deadlines are
// retained with the same exchange record.
type MailboxExchangeState string

const (
	MailboxExchangeAccepted      MailboxExchangeState = "accepted"
	MailboxExchangeComplete      MailboxExchangeState = "complete"
	MailboxExchangeRejected      MailboxExchangeState = "rejected"
	MailboxExchangeIndeterminate MailboxExchangeState = "indeterminate"
)

// MailboxExecutionSelection is the trusted, immutable new-work decision made
// by the mailbox processor before it creates a local intent. It is persisted
// with the exchange so retained retries and later configuration edits cannot
// change the effective target of accepted work.
//
// Repository fields are labels only. They never contain a source path or
// cause source materialization.
type MailboxExecutionSelection struct {
	ContextName       string
	Environment       string
	Target            domain.ExecutionTarget
	Source            string
	RepositoryAlias   string
	RepositoryAliases []string
}

// MailboxExchangeCreate contains the immutable receipt binding. CanonicalPayload
// must exclude request_id and idempotency_key for mutation retries.
type MailboxExchangeCreate struct {
	// MailboxID is the durable namespace. New mailbox runtimes supply it
	// explicitly; legacy callers without a namespace use the default wrapper.
	MailboxID      string
	RequestID      string
	Operation      string
	Controller     domain.ControllerIdentity
	IdempotencyKey string
	// ExecutionIdempotencyKey is the trusted internal key passed to local
	// intents and remote frames. It is never rendered in mailbox JSON.
	ExecutionIdempotencyKey string
	RequestHash             domain.CanonicalHash
	CanonicalPayload        []byte
	ResourceID              string
	// Selection is present only for a successfully resolved create_session or
	// run request. Nil preserves compatibility with legacy exchanges and
	// non-new-work operations.
	Selection      *MailboxExecutionSelection
	SelectionState MailboxExecutionSelectionState
}

// MailboxExchangeRecord is the durable request receipt, current response, and
// cleanup lifecycle for its immutable request ID.
type MailboxExchangeRecord struct {
	MailboxID string
	// ExchangeID is an internal durable key used by foreign keys and cleanup.
	// It is never exposed by the mailbox wire protocol.
	ExchangeID               string
	RequestID                string
	Operation                string
	Controller               domain.ControllerIdentity
	IdempotencyKey           string
	ExecutionIdempotencyKey  string
	RequestHash              domain.CanonicalHash
	CanonicalPayload         []byte
	ResourceID               string
	Selection                *MailboxExecutionSelection
	SelectionState           MailboxExecutionSelectionState
	State                    MailboxExchangeState
	ResponseRevision         int64
	ResponseBytes            []byte
	ResponseSHA256           []byte
	TerminalResponseBytes    []byte
	TerminalResponseSHA256   []byte
	AvailableEventSequence   *int64
	AcknowledgedAt           *time.Time
	ResponseCleanupAt        *time.Time
	ResponseCleanupStartedAt *time.Time
	ResponseFileRemovedAt    *time.Time
	EventFileCommandID       string
	IdempotencyKeyExpiresAt  *time.Time
	IdempotencyBindingActive bool
	DeduplicationWarning     bool
	CreatedAt                time.Time
	UpdatedAt                time.Time
}

// MailboxTerminalArtifactCursor identifies the last durable exchange examined
// by a bounded terminal-artifact recovery pass. It is intentionally based on
// the stable storage ordering rather than a client-visible request ID, which
// is only unique inside its mailbox namespace.
type MailboxTerminalArtifactCursor struct {
	CreatedAt  time.Time
	ExchangeID string
}

// MailboxResponsePublication is one response snapshot. Accepted snapshots
// may be replaced by a higher revision; terminal snapshots become immutable.
type MailboxResponsePublication struct {
	State                  MailboxExchangeState
	Bytes                  []byte
	AvailableEventSequence *int64
}

// ErrMailboxRemoteStatusFailureNotEligible means a concurrent successful
// reconciliation changed an accepted remote run before its status-unavailable
// mailbox outcome could be frozen. The caller must reload the ordinary result;
// it must not overwrite it with an indeterminate response.
var ErrMailboxRemoteStatusFailureNotEligible = errors.New("mailbox remote status failure is no longer eligible")

// AcceptMailboxExchange binds one request_id in the same SQLite transaction
// used to detect a retained same-key retry. Reusing a request_id with changed
// semantics conflicts. A new request_id with the same operation/controller/
// idempotency key and hash receives the original binding and state without
// invoking a mutation a second time.
func (s *AuthorityStore) AcceptMailboxExchange(ctx context.Context, input MailboxExchangeCreate) (record MailboxExchangeRecord, duplicate bool, err error) {
	record, duplicate, _, err = s.acceptMailboxExchange(ctx, input, false, false, false)
	return record, duplicate, err
}

// AcceptMailboxExchangeInMailbox binds a receipt to an explicit mailbox
// namespace. Mailbox runtimes must use this method; the unscoped method above
// remains only as a default-mailbox compatibility boundary.
func (s *AuthorityStore) AcceptMailboxExchangeInMailbox(ctx context.Context, ref MailboxExchangeRef, input MailboxExchangeCreate) (record MailboxExchangeRecord, duplicate bool, err error) {
	input, err = mailboxRefWithInput(ref, input)
	if err != nil {
		return MailboxExchangeRecord{}, false, err
	}
	record, duplicate, _, err = s.acceptMailboxExchange(ctx, input, false, false, true)
	return record, duplicate, err
}

// AcceptMailboxExchangeWithConflictReceipt atomically records a changed-payload
// request ID when its key is still active, without replacing the original key
// binding. The final bool identifies that the new request must receive a
// structured idempotency_conflict response and must not invoke an operation.
func (s *AuthorityStore) AcceptMailboxExchangeWithConflictReceipt(ctx context.Context, input MailboxExchangeCreate) (record MailboxExchangeRecord, duplicate, idempotencyConflict bool, err error) {
	return s.acceptMailboxExchange(ctx, input, true, true, false)
}

// AcceptMailboxExchangeWithConflictReceiptInMailbox is the explicit mailbox
// variant used by the session processor.
func (s *AuthorityStore) AcceptMailboxExchangeWithConflictReceiptInMailbox(ctx context.Context, ref MailboxExchangeRef, input MailboxExchangeCreate) (record MailboxExchangeRecord, duplicate, idempotencyConflict bool, err error) {
	input, err = mailboxRefWithInput(ref, input)
	if err != nil {
		return MailboxExchangeRecord{}, false, false, err
	}
	return s.acceptMailboxExchange(ctx, input, true, true, true)
}

func (s *AuthorityStore) acceptMailboxExchange(ctx context.Context, input MailboxExchangeCreate, recordConflict, refreshDuplicate, requireExplicitMailbox bool) (record MailboxExchangeRecord, duplicate, idempotencyConflict bool, err error) {
	_, input, err = mailboxRefFromInput(input, requireExplicitMailbox)
	if err != nil {
		return MailboxExchangeRecord{}, false, false, err
	}
	validated, err := validateMailboxExchangeCreate(input)
	if err != nil {
		return MailboxExchangeRecord{}, false, false, err
	}
	now := s.now().UTC()
	returnValue, err := withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) (MailboxExchangeRecord, error) {
		if err := rejectMailboxExchangeIngressDiagnosticCollision(ctx, connection, validated.MailboxExchangeRef()); err != nil {
			return MailboxExchangeRecord{}, err
		}
		byID, found, err := readMailboxExchangeByIDOnConnection(ctx, connection, validated.MailboxExchangeRef())
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
			existing, found, err := readLatestMailboxExchangeByKeyOnConnection(ctx, connection, validated.MailboxID, validated.Controller, validated.Operation, validated.IdempotencyKey)
			if err != nil {
				return MailboxExchangeRecord{}, err
			}
			if found {
				if existing.IdempotencyKeyExpiresAt != nil && now.Before(*existing.IdempotencyKeyExpiresAt) {
					if domain.CompareIdempotency(existing.RequestHash, validated.RequestHash) == domain.IdempotencyConflict || !bytesEqual(existing.CanonicalPayload, validated.CanonicalPayload) {
						if !recordConflict {
							return MailboxExchangeRecord{}, ErrIdempotencyConflict
						}
						validated.State = MailboxExchangeAccepted
						validated.IdempotencyBindingActive = false
						validated.CreatedAt, validated.UpdatedAt = now, now
						if err := insertMailboxExchangeOnConnection(ctx, connection, validated); err != nil {
							return MailboxExchangeRecord{}, err
						}
						idempotencyConflict = true
						return readMailboxExchangeOnConnection(ctx, connection, validated.MailboxExchangeRef())
					}
					validated.ResourceID = existing.ResourceID
					// The original acceptance owns the selection snapshot. A retry
					// must not obtain a newer default or alter the selection source.
					validated.Selection = cloneMailboxExecutionSelection(existing.Selection)
					validated.SelectionState = existing.SelectionState
					if refreshDuplicate {
						// The session processor creates a new response snapshot and asks
						// its operation adapter for the original resource's current view.
						validated.IdempotencyKeyExpiresAt = existing.IdempotencyKeyExpiresAt
						validated.IdempotencyBindingActive = true
						validated.DeduplicationWarning = existing.DeduplicationWarning
					} else {
						// Generic receipt users retain the P082/P083 replay contract.
						validated.State = existing.State
						validated.ResponseRevision = existing.ResponseRevision
						if len(existing.ResponseBytes) > 0 {
							validated.ResponseBytes, err = rebindMailboxResponseRequestID(existing.ResponseBytes, validated.RequestID)
							if err != nil {
								return MailboxExchangeRecord{}, err
							}
							validated.ResponseSHA256 = sha256Bytes(validated.ResponseBytes)
						}
						if len(existing.TerminalResponseBytes) > 0 {
							validated.TerminalResponseBytes, err = rebindMailboxResponseRequestID(existing.TerminalResponseBytes, validated.RequestID)
							if err != nil {
								return MailboxExchangeRecord{}, err
							}
							validated.TerminalResponseSHA256 = sha256Bytes(validated.TerminalResponseBytes)
						}
						if existing.AvailableEventSequence != nil {
							cursor := *existing.AvailableEventSequence
							validated.AvailableEventSequence = &cursor
						}
					}
					validated.IdempotencyKeyExpiresAt = existing.IdempotencyKeyExpiresAt
					validated.IdempotencyBindingActive = true
					validated.DeduplicationWarning = existing.DeduplicationWarning
					duplicate = true
					if !refreshDuplicate {
						validated.CreatedAt, validated.UpdatedAt = now, now
						if err := insertMailboxExchangeOnConnection(ctx, connection, validated); err != nil {
							return MailboxExchangeRecord{}, err
						}
						return readMailboxExchangeOnConnection(ctx, connection, validated.MailboxExchangeRef())
					}
				} else {
					// The mapping expired. This is a new operation, and its response
					// must warn that duplicate prevention is no longer guaranteed.
					validated.DeduplicationWarning = true
				}
			}
			if !validated.IdempotencyBindingActive {
				validated.IdempotencyBindingActive = true
				validated.IdempotencyKeyExpiresAt = timePointerMailboxStore(now.Add(DefaultSessionIdempotencyRetention))
			}
		}
		validated.State = MailboxExchangeAccepted
		validated.CreatedAt = now
		validated.UpdatedAt = now
		if err := insertMailboxExchangeOnConnection(ctx, connection, validated); err != nil {
			return MailboxExchangeRecord{}, err
		}
		return readMailboxExchangeOnConnection(ctx, connection, validated.MailboxExchangeRef())
	})
	if err != nil {
		return MailboxExchangeRecord{}, false, false, err
	}
	return returnValue, duplicate, idempotencyConflict, nil
}

func timePointerMailboxStore(value time.Time) *time.Time {
	value = value.UTC()
	return &value
}

func rebindMailboxResponseRequestID(raw []byte, requestID string) ([]byte, error) {
	var response map[string]json.RawMessage
	if err := json.Unmarshal(raw, &response); err != nil || response == nil {
		return nil, fmt.Errorf("%w: stored response is not a JSON object", ErrMailboxResponseInvalid)
	}
	requestIDBytes, err := json.Marshal(requestID)
	if err != nil {
		return nil, fmt.Errorf("%w: encode response request ID: %v", ErrMailboxResponseInvalid, err)
	}
	response["request_id"] = requestIDBytes
	result, err := json.Marshal(response)
	if err != nil {
		return nil, fmt.Errorf("%w: encode correlated response: %v", ErrMailboxResponseInvalid, err)
	}
	return result, nil
}

// GetMailboxExchange reloads the default-mailbox compatibility receipt.
func (s *AuthorityStore) GetMailboxExchange(ctx context.Context, requestID string) (MailboxExchangeRecord, error) {
	ref, err := defaultMailboxExchangeRef(requestID)
	if err != nil {
		return MailboxExchangeRecord{}, err
	}
	return s.GetMailboxExchangeInMailbox(ctx, ref)
}

// GetMailboxExchangeInMailbox reloads one durable receipt in its mailbox
// namespace. It never searches another mailbox with the same client ID.
func (s *AuthorityStore) GetMailboxExchangeInMailbox(ctx context.Context, ref MailboxExchangeRef) (MailboxExchangeRecord, error) {
	validated, err := validateMailboxExchangeRef(ref)
	if err != nil {
		return MailboxExchangeRecord{}, err
	}
	return withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) (MailboxExchangeRecord, error) {
		return readMailboxExchangeOnConnection(ctx, connection, validated)
	})
}

// FindActiveMailboxExchangeByKeyInMailbox returns the unexpired, active
// idempotency binding for one mailbox/controller/operation/key tuple. It is
// used before canonicalizing an omitted mailbox execution selection so a
// retained retry keeps the target that was accepted originally.
func (s *AuthorityStore) FindActiveMailboxExchangeByKeyInMailbox(ctx context.Context, mailboxID string, controller domain.ControllerIdentity, operation, key string) (MailboxExchangeRecord, bool, error) {
	if err := validateMailboxID(mailboxID); err != nil {
		return MailboxExchangeRecord{}, false, err
	}
	validatedController, err := validateController(controller)
	if err != nil {
		return MailboxExchangeRecord{}, false, fmt.Errorf("%w: controller: %v", ErrMailboxExchangeInvalid, err)
	}
	if operation == "" || len(operation) > 128 || strings.IndexByte(operation, 0) >= 0 {
		return MailboxExchangeRecord{}, false, fmt.Errorf("%w: operation", ErrMailboxExchangeInvalid)
	}
	if key == "" || len(key) > 256 || strings.IndexByte(key, 0) >= 0 {
		return MailboxExchangeRecord{}, false, fmt.Errorf("%w: idempotency key", ErrMailboxExchangeInvalid)
	}
	now := s.now().UTC()
	record, err := withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) (MailboxExchangeRecord, error) {
		record, found, err := readLatestMailboxExchangeByKeyOnConnection(ctx, connection, mailboxID, validatedController, operation, key)
		if err != nil || !found {
			return record, err
		}
		if !record.IdempotencyBindingActive || record.IdempotencyKeyExpiresAt == nil || !now.Before(*record.IdempotencyKeyExpiresAt) {
			return MailboxExchangeRecord{}, nil
		}
		return record, nil
	})
	if err != nil {
		return MailboxExchangeRecord{}, false, err
	}
	if record.ExchangeID == "" {
		return MailboxExchangeRecord{}, false, nil
	}
	return record, true, nil
}

// ListMailboxExchanges returns mailbox exchanges for one controller and
// operation in stable creation order. It lets operation projectors resume
// accepted asynchronous requests after restart.
func (s *AuthorityStore) ListMailboxExchanges(ctx context.Context, controller domain.ControllerIdentity, operation string, state MailboxExchangeState) ([]MailboxExchangeRecord, error) {
	return s.ListMailboxExchangesInMailbox(ctx, DefaultMailboxID, controller, operation, state)
}

// ListMailboxExchangesInMailbox returns exchanges from only one mailbox.
func (s *AuthorityStore) ListMailboxExchangesInMailbox(ctx context.Context, mailboxID string, controller domain.ControllerIdentity, operation string, state MailboxExchangeState) ([]MailboxExchangeRecord, error) {
	if err := validateMailboxID(mailboxID); err != nil {
		return nil, err
	}
	validatedController, err := validateController(controller)
	if err != nil {
		return nil, fmt.Errorf("%w: controller: %v", ErrMailboxExchangeInvalid, err)
	}
	if strings.TrimSpace(operation) == "" || len(operation) > 128 || strings.IndexByte(operation, 0) >= 0 {
		return nil, fmt.Errorf("%w: operation", ErrMailboxExchangeInvalid)
	}
	if state != MailboxExchangeAccepted && state != MailboxExchangeComplete && state != MailboxExchangeRejected && state != MailboxExchangeIndeterminate {
		return nil, fmt.Errorf("%w: state %q", ErrMailboxExchangeInvalid, state)
	}
	return withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) ([]MailboxExchangeRecord, error) {
		rows, err := connection.QueryContext(ctx, `
		SELECT client_request_id FROM mailbox_exchanges
	WHERE mailbox_id = ? AND controller_type = ? AND controller_id = ? AND operation = ? AND request_state = ?
	ORDER BY created_at, exchange_id
	`, mailboxID, string(validatedController.Type()), string(validatedController.ID()), operation, string(state))
		if err != nil {
			return nil, fmt.Errorf("list mailbox exchanges: %w", err)
		}
		var requestIDs []string
		for rows.Next() {
			var requestID string
			if err := rows.Scan(&requestID); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan mailbox request ID: %w", err)
			}
			requestIDs = append(requestIDs, requestID)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("read mailbox request IDs: %w", err)
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("close mailbox request IDs: %w", err)
		}
		records := make([]MailboxExchangeRecord, 0, len(requestIDs))
		for _, requestID := range requestIDs {
			record, err := readMailboxExchangeOnConnection(ctx, connection, MailboxExchangeRef{MailboxID: mailboxID, ClientRequestID: requestID})
			if err != nil {
				return nil, err
			}
			records = append(records, record)
		}
		return records, nil
	})
}

// ListMailboxExchangesForIntent returns every mailbox receipt bound to one
// local intent. The durable join is the private execution idempotency key:
// mailbox acceptance records it before the local intent is created, and the
// scoped request supplies that same key to the local API. This lets a later
// remote reconciliation find its file-ingress receipt after a restart without
// exposing the key, payload, script, or response body.
func (s *AuthorityStore) ListMailboxExchangesForIntent(ctx context.Context, controller domain.ControllerIdentity, operation string, intentID domain.IntentID) ([]MailboxExchangeRecord, error) {
	validatedController, err := validateController(controller)
	if err != nil {
		return nil, fmt.Errorf("%w: controller: %v", ErrMailboxExchangeInvalid, err)
	}
	if strings.TrimSpace(operation) == "" || len(operation) > 128 || strings.IndexByte(operation, 0) >= 0 {
		return nil, fmt.Errorf("%w: operation", ErrMailboxExchangeInvalid)
	}
	validatedIntentID, err := domain.NewIntentID(string(intentID))
	if err != nil {
		return nil, fmt.Errorf("%w: intent ID", ErrMailboxExchangeInvalid)
	}
	return withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) ([]MailboxExchangeRecord, error) {
		rows, err := connection.QueryContext(ctx, `
		SELECT exchange.mailbox_id, exchange.client_request_id
		FROM mailbox_exchanges AS exchange
		JOIN local_intents AS local_intent
		  ON local_intent.idempotency_key = exchange.execution_idempotency_key
		WHERE exchange.controller_type = ? AND exchange.controller_id = ? AND exchange.operation = ?
		  AND local_intent.intent_id = ? AND local_intent.controller_type = ? AND local_intent.controller_id = ? AND local_intent.operation = ?
		ORDER BY exchange.mailbox_id, exchange.created_at, exchange.exchange_id
		`, string(validatedController.Type()), string(validatedController.ID()), operation,
			string(validatedIntentID), string(validatedController.Type()), string(validatedController.ID()), operation)
		if err != nil {
			return nil, fmt.Errorf("list mailbox exchanges for intent: %w", err)
		}
		defer rows.Close()
		refs := make([]MailboxExchangeRef, 0)
		for rows.Next() {
			var mailboxID, requestID string
			if err := rows.Scan(&mailboxID, &requestID); err != nil {
				return nil, fmt.Errorf("scan mailbox exchange intent reference: %w", err)
			}
			refs = append(refs, MailboxExchangeRef{MailboxID: mailboxID, ClientRequestID: requestID})
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("read mailbox exchange intent references: %w", err)
		}
		records := make([]MailboxExchangeRecord, 0, len(refs))
		for _, ref := range refs {
			record, err := readMailboxExchangeOnConnection(ctx, connection, ref)
			if err != nil {
				return nil, err
			}
			records = append(records, record)
		}
		return records, nil
	})
}

// ListPublishableTerminalMailboxExchanges returns terminal responses that are
// still within their retention period and have not been claimed for cleanup.
// A mailbox projector uses this to rebuild derived outbox and event files
// after a process stop between the SQLite commit and the filesystem rename.
// Cleanup ownership wins over recovery: once cleanup has started, a restart
// must never recreate the response.
func (s *AuthorityStore) ListPublishableTerminalMailboxExchanges(ctx context.Context, controller domain.ControllerIdentity) ([]MailboxExchangeRecord, error) {
	return s.ListPublishableTerminalMailboxExchangesInMailbox(ctx, DefaultMailboxID, controller)
}

// ListPublishableTerminalMailboxExchangesInMailbox returns recoverable
// terminal response projections from only one mailbox root.
func (s *AuthorityStore) ListPublishableTerminalMailboxExchangesInMailbox(ctx context.Context, mailboxID string, controller domain.ControllerIdentity) ([]MailboxExchangeRecord, error) {
	return s.listPublishableTerminalMailboxExchangesInMailbox(ctx, mailboxID, controller, nil, 0)
}

// ListPublishableTerminalMailboxExchangesPageInMailbox returns at most limit
// recoverable terminal response projections from one mailbox root, beginning
// strictly after after in created_at/exchange_id order. A nil cursor begins at
// the oldest eligible exchange. It is for a bounded recovery pass; callers
// that need the historical compatibility behavior should use the unbounded
// ListPublishableTerminalMailboxExchangesInMailbox method above.
func (s *AuthorityStore) ListPublishableTerminalMailboxExchangesPageInMailbox(ctx context.Context, mailboxID string, controller domain.ControllerIdentity, after *MailboxTerminalArtifactCursor, limit int) ([]MailboxExchangeRecord, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("%w: terminal mailbox exchange page limit", ErrMailboxExchangeInvalid)
	}
	return s.listPublishableTerminalMailboxExchangesInMailbox(ctx, mailboxID, controller, after, limit)
}

func (s *AuthorityStore) listPublishableTerminalMailboxExchangesInMailbox(ctx context.Context, mailboxID string, controller domain.ControllerIdentity, after *MailboxTerminalArtifactCursor, limit int) ([]MailboxExchangeRecord, error) {
	if err := validateMailboxID(mailboxID); err != nil {
		return nil, err
	}
	validatedController, err := validateController(controller)
	if err != nil {
		return nil, fmt.Errorf("%w: controller: %v", ErrMailboxExchangeInvalid, err)
	}
	validatedAfter, err := validateMailboxTerminalArtifactCursor(after)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	storedNow := formatStoredTime(now)
	legacyUnackedLifetimeDays := MailboxUnackedResponseLifetime.Hours() / (24 * time.Hour).Hours()
	legacyAckedLifetimeDays := MailboxAckedResponseLifetime.Hours() / (24 * time.Hour).Hours()
	return withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) ([]MailboxExchangeRecord, error) {
		var rows *sql.Rows
		if limit == 0 {
			rows, err = connection.QueryContext(ctx, `
		SELECT client_request_id FROM mailbox_exchanges
	WHERE mailbox_id = ? AND controller_type = ? AND controller_id = ?
  AND request_state IN ('complete', 'rejected', 'indeterminate')
	  AND response_revision > 0 AND length(response_bytes) > 0
	  AND response_cleanup_started_at IS NULL AND response_file_removed_at IS NULL
	  AND (response_cleanup_at IS NULL OR response_cleanup_at > ?)
		ORDER BY created_at, exchange_id
		`, mailboxID, string(validatedController.Type()), string(validatedController.ID()), storedNow)
		} else if validatedAfter == nil {
			rows, err = connection.QueryContext(ctx, `
		SELECT client_request_id FROM mailbox_exchanges
	WHERE mailbox_id = ? AND controller_type = ? AND controller_id = ?
  AND request_state IN ('complete', 'rejected', 'indeterminate')
  AND response_revision > 0 AND length(response_bytes) > 0
  AND response_cleanup_started_at IS NULL AND response_file_removed_at IS NULL
  AND (
    response_cleanup_at > ?
    OR (
      response_cleanup_at IS NULL
      AND julianday(updated_at) + ? > julianday(?)
      AND (acknowledged_at IS NULL OR julianday(acknowledged_at) + ? > julianday(?))
    )
  )
		ORDER BY created_at, exchange_id
		LIMIT ?
		`, mailboxID, string(validatedController.Type()), string(validatedController.ID()), storedNow, legacyUnackedLifetimeDays, storedNow, legacyAckedLifetimeDays, storedNow, limit)
		} else {
			cursorTime := formatStoredTime(validatedAfter.CreatedAt)
			rows, err = connection.QueryContext(ctx, `
		SELECT client_request_id FROM mailbox_exchanges
	WHERE mailbox_id = ? AND controller_type = ? AND controller_id = ?
  AND request_state IN ('complete', 'rejected', 'indeterminate')
  AND response_revision > 0 AND length(response_bytes) > 0
  AND response_cleanup_started_at IS NULL AND response_file_removed_at IS NULL
  AND (
    response_cleanup_at > ?
    OR (
      response_cleanup_at IS NULL
      AND julianday(updated_at) + ? > julianday(?)
      AND (acknowledged_at IS NULL OR julianday(acknowledged_at) + ? > julianday(?))
    )
  )
  AND (created_at > ? OR (created_at = ? AND exchange_id > ?))
		ORDER BY created_at, exchange_id
		LIMIT ?
		`, mailboxID, string(validatedController.Type()), string(validatedController.ID()), storedNow, legacyUnackedLifetimeDays, storedNow, legacyAckedLifetimeDays, storedNow, cursorTime, cursorTime, validatedAfter.ExchangeID, limit)
		}
		if err != nil {
			return nil, fmt.Errorf("list publishable terminal mailbox exchanges: %w", err)
		}
		defer rows.Close()
		var requestIDs []string
		for rows.Next() {
			var requestID string
			if err := rows.Scan(&requestID); err != nil {
				return nil, fmt.Errorf("scan publishable terminal mailbox request ID: %w", err)
			}
			requestIDs = append(requestIDs, requestID)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("read publishable terminal mailbox request IDs: %w", err)
		}
		records := make([]MailboxExchangeRecord, 0, len(requestIDs))
		for _, requestID := range requestIDs {
			record, err := readMailboxExchangeOnConnection(ctx, connection, MailboxExchangeRef{MailboxID: mailboxID, ClientRequestID: requestID})
			if err != nil {
				return nil, err
			}
			// Pre-P091 terminal rows have no stored cleanup deadline. The reader
			// derives their historical deadline from the durable publication or ACK
			// time, so apply the same retention gate after loading them.
			if record.ResponseCleanupAt == nil || !record.ResponseCleanupAt.After(now) {
				continue
			}
			records = append(records, record)
		}
		return records, nil
	})
}

func validateMailboxTerminalArtifactCursor(cursor *MailboxTerminalArtifactCursor) (*MailboxTerminalArtifactCursor, error) {
	if cursor == nil {
		return nil, nil
	}
	if cursor.CreatedAt.IsZero() || cursor.ExchangeID == "" || strings.IndexByte(cursor.ExchangeID, 0) >= 0 {
		return nil, fmt.Errorf("%w: terminal mailbox exchange cursor", ErrMailboxExchangeInvalid)
	}
	validated := *cursor
	validated.CreatedAt = validated.CreatedAt.UTC()
	return &validated, nil
}

// CompleteMailboxExchange advances an accepted receipt to one terminal
// mailbox state. Repeating the same terminal state is idempotent; changing a
// terminal outcome is rejected so a later response phase cannot rewrite it.
func (s *AuthorityStore) CompleteMailboxExchange(ctx context.Context, requestID string, next MailboxExchangeState) (MailboxExchangeRecord, error) {
	ref, err := defaultMailboxExchangeRef(requestID)
	if err != nil {
		return MailboxExchangeRecord{}, err
	}
	return s.CompleteMailboxExchangeInMailbox(ctx, ref, next)
}

// CompleteMailboxExchangeInMailbox advances a receipt in one mailbox only.
func (s *AuthorityStore) CompleteMailboxExchangeInMailbox(ctx context.Context, ref MailboxExchangeRef, next MailboxExchangeState) (MailboxExchangeRecord, error) {
	validated, err := validateMailboxExchangeRef(ref)
	if err != nil {
		return MailboxExchangeRecord{}, err
	}
	if next != MailboxExchangeComplete && next != MailboxExchangeRejected && next != MailboxExchangeIndeterminate {
		return MailboxExchangeRecord{}, fmt.Errorf("%w: terminal state %q", ErrMailboxExchangeInvalid, next)
	}
	now := s.now().UTC()
	return withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) (MailboxExchangeRecord, error) {
		current, err := readMailboxExchangeOnConnection(ctx, connection, validated)
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
	WHERE mailbox_id = ? AND controller_type = ? AND controller_id = ? AND operation = ?
	  AND client_idempotency_key = ? AND canonical_hash_version = ? AND canonical_hash = ?
	`, string(next), formatStoredTime(now), current.MailboxID, string(current.Controller.Type()), string(current.Controller.ID()), current.Operation, current.IdempotencyKey, current.RequestHash.Version(), current.RequestHash.SHA256()); err != nil {
			return MailboxExchangeRecord{}, fmt.Errorf("complete mailbox exchange: %w", err)
		}
		return readMailboxExchangeOnConnection(ctx, connection, validated)
	})
}

// PublishMailboxResponse stores one response revision. Nonterminal snapshots
// increment response_revision; a terminal snapshot is copied into immutable
// terminal columns and cannot be changed by a later retry of the same ID.
func (s *AuthorityStore) PublishMailboxResponse(ctx context.Context, requestID string, publication MailboxResponsePublication) (MailboxExchangeRecord, error) {
	ref, err := defaultMailboxExchangeRef(requestID)
	if err != nil {
		return MailboxExchangeRecord{}, err
	}
	return s.PublishMailboxResponseInMailbox(ctx, ref, publication)
}

// PublishMailboxResponseInMailbox stores one response revision in the
// exchange selected by its explicit mailbox/client identity.
func (s *AuthorityStore) PublishMailboxResponseInMailbox(ctx context.Context, ref MailboxExchangeRef, publication MailboxResponsePublication) (MailboxExchangeRecord, error) {
	validated, err := validateMailboxExchangeRef(ref)
	if err != nil {
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
	var eventFileCommandID domain.CommandID
	bindEventFile := false
	if publication.State != MailboxExchangeAccepted && publication.AvailableEventSequence != nil && *publication.AvailableEventSequence > 0 {
		parsedCommandID, shouldBind, parseErr := responseEventFileCommand(publication.Bytes)
		if parseErr != nil {
			return MailboxExchangeRecord{}, parseErr
		}
		eventFileCommandID, bindEventFile = parsedCommandID, shouldBind
	}
	responseHash := sha256Bytes(publication.Bytes)
	now := s.now().UTC()
	return withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) (MailboxExchangeRecord, error) {
		current, err := readMailboxExchangeOnConnection(ctx, connection, validated)
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
		var terminalBytes, terminalHash, responseCleanupAt any
		if publication.State == MailboxExchangeComplete || publication.State == MailboxExchangeRejected || publication.State == MailboxExchangeIndeterminate {
			terminalBytes, terminalHash = publication.Bytes, responseHash
			responseCleanupAt = formatStoredTime(now.Add(MailboxUnackedResponseLifetime))
		}
		if _, err := connection.ExecContext(ctx, `
UPDATE mailbox_exchanges
SET request_state = ?, response_revision = ?, response_bytes = ?, response_sha256 = ?,
    terminal_response_bytes = ?, terminal_response_sha256 = ?, available_event_sequence = ?,
    response_cleanup_at = ?, updated_at = ?
	WHERE exchange_id = ? AND request_state = 'accepted'
	`, string(publication.State), nextRevision, publication.Bytes, responseHash, terminalBytes, terminalHash, publication.AvailableEventSequence, responseCleanupAt, formatStoredTime(now), current.ExchangeID); err != nil {
			return MailboxExchangeRecord{}, fmt.Errorf("publish mailbox response: %w", err)
		}
		if bindEventFile {
			if err := bindMailboxEventFileReferenceOnConnection(ctx, connection, validated, eventFileCommandID, now); err != nil {
				return MailboxExchangeRecord{}, err
			}
		}
		return readMailboxExchangeOnConnection(ctx, connection, validated)
	})
}

// PublishAcceptedRemoteStatusUnavailableInMailbox atomically freezes the
// bounded diagnostic outcome for an accepted remote run. It verifies that the
// same local intent is still accepted, still has no strict terminal proof,
// and still carries the durable status-failure marker. That prevents a racing
// successful reconciliation from being replaced by an indeterminate receipt.
func (s *AuthorityStore) PublishAcceptedRemoteStatusUnavailableInMailbox(ctx context.Context, ref MailboxExchangeRef, publication MailboxResponsePublication) (MailboxExchangeRecord, error) {
	validated, err := validateMailboxExchangeRef(ref)
	if err != nil {
		return MailboxExchangeRecord{}, err
	}
	if publication.State != MailboxExchangeIndeterminate || len(publication.Bytes) == 0 || len(publication.Bytes) > domain.MaxSerializedRequestBytes || publication.AvailableEventSequence != nil {
		return MailboxExchangeRecord{}, fmt.Errorf("%w: remote status failure publication", ErrMailboxResponseInvalid)
	}
	responseHash := sha256Bytes(publication.Bytes)
	now := s.now().UTC()
	return withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) (MailboxExchangeRecord, error) {
		current, err := readMailboxExchangeOnConnection(ctx, connection, validated)
		if err != nil {
			return MailboxExchangeRecord{}, err
		}
		if current.State != MailboxExchangeAccepted {
			return MailboxExchangeRecord{}, ErrMailboxTerminalImmutable
		}
		var acceptedResponse struct {
			Operation     string `json:"operation"`
			RequestState  string `json:"request_state"`
			JobID         string `json:"job_id"`
			SessionID     string `json:"session_id"`
			CommandID     string `json:"command_id"`
			DeliveryState string `json:"delivery_state"`
		}
		if current.Operation != "run" || json.Unmarshal(current.ResponseBytes, &acceptedResponse) != nil ||
			acceptedResponse.Operation != "run" || acceptedResponse.RequestState != string(MailboxExchangeAccepted) ||
			acceptedResponse.DeliveryState != string(LocalIntentAccepted) {
			return MailboxExchangeRecord{}, ErrMailboxRemoteStatusFailureNotEligible
		}
		jobID, jobErr := domain.NewJobID(acceptedResponse.JobID)
		sessionID, sessionErr := domain.NewSessionID(acceptedResponse.SessionID)
		commandID, commandErr := domain.NewCommandID(acceptedResponse.CommandID)
		if jobErr != nil || sessionErr != nil || commandErr != nil {
			return MailboxExchangeRecord{}, ErrMailboxRemoteStatusFailureNotEligible
		}
		var intentID, targetKind, deliveryState, intentSessionID, intentCommandID string
		var proofVersion int
		err = connection.QueryRowContext(ctx, `
SELECT intent_id, target_kind, delivery_state, remote_terminal_proof_version, session_id, command_id
FROM local_intents
WHERE operation = 'run' AND resource_id = ? AND idempotency_key = ? AND controller_type = ? AND controller_id = ?
ORDER BY created_at DESC, intent_id DESC
LIMIT 1
`, string(jobID), current.ExecutionIdempotencyKey, string(current.Controller.Type()), string(current.Controller.ID())).Scan(&intentID, &targetKind, &deliveryState, &proofVersion, &intentSessionID, &intentCommandID)
		if errors.Is(err, sql.ErrNoRows) {
			return MailboxExchangeRecord{}, ErrMailboxRemoteStatusFailureNotEligible
		}
		if err != nil {
			return MailboxExchangeRecord{}, fmt.Errorf("read remote status failure intent for mailbox: %w", err)
		}
		if targetKind != string(domain.TargetKindRemote) || deliveryState != string(LocalIntentAccepted) || proofVersion >= RemoteTerminalProofP149 ||
			intentSessionID != string(sessionID) || intentCommandID != string(commandID) {
			return MailboxExchangeRecord{}, ErrMailboxRemoteStatusFailureNotEligible
		}
		var code string
		err = connection.QueryRowContext(ctx, `
SELECT reason FROM local_remote_status_failures WHERE intent_id = ?
`, intentID).Scan(&code)
		if errors.Is(err, sql.ErrNoRows) || code != RemoteStatusFailureCodeUnavailable {
			return MailboxExchangeRecord{}, ErrMailboxRemoteStatusFailureNotEligible
		}
		if err != nil {
			return MailboxExchangeRecord{}, fmt.Errorf("read remote status failure for mailbox: %w", err)
		}
		nextRevision := current.ResponseRevision + 1
		if nextRevision <= 0 {
			return MailboxExchangeRecord{}, fmt.Errorf("%w: response revision overflow", ErrMailboxResponseInvalid)
		}
		if _, err := connection.ExecContext(ctx, `
UPDATE mailbox_exchanges
SET request_state = ?, response_revision = ?, response_bytes = ?, response_sha256 = ?,
    terminal_response_bytes = ?, terminal_response_sha256 = ?, available_event_sequence = NULL,
    response_cleanup_at = ?, updated_at = ?
WHERE exchange_id = ? AND request_state = 'accepted'
`, string(MailboxExchangeIndeterminate), nextRevision, publication.Bytes, responseHash,
			publication.Bytes, responseHash, formatStoredTime(now.Add(MailboxUnackedResponseLifetime)), formatStoredTime(now), current.ExchangeID); err != nil {
			return MailboxExchangeRecord{}, fmt.Errorf("publish remote status unavailable mailbox response: %w", err)
		}
		return readMailboxExchangeOnConnection(ctx, connection, validated)
	})
}

func responseEventFileCommand(response []byte) (domain.CommandID, bool, error) {
	var reference struct {
		CommandID  string `json:"command_id"`
		EventsFile string `json:"events_file"`
	}
	if err := json.Unmarshal(response, &reference); err != nil {
		return "", false, nil
	}
	if reference.CommandID == "" && reference.EventsFile == "" {
		return "", false, nil
	}
	commandID, err := domain.NewCommandID(reference.CommandID)
	if err != nil || reference.EventsFile != "events/"+reference.CommandID+".ndjson" {
		return "", false, fmt.Errorf("%w: event-file command/path reference", ErrMailboxResponseInvalid)
	}
	return commandID, true, nil
}

func validateMailboxExchangeCreate(input MailboxExchangeCreate) (validatedMailboxExchangeCreate, error) {
	ref, err := NewMailboxExchangeRef(input.MailboxID, input.RequestID)
	if err != nil {
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
	executionKey := mailboxExecutionIdempotencyKey(ref.MailboxID, input.IdempotencyKey)
	if input.ExecutionIdempotencyKey != "" && input.ExecutionIdempotencyKey != executionKey {
		return validatedMailboxExchangeCreate{}, fmt.Errorf("%w: execution idempotency key is not derived from mailbox scope", ErrMailboxExchangeInvalid)
	}
	if err := validateExecutionIdempotencyKey(executionKey); err != nil {
		return validatedMailboxExchangeCreate{}, fmt.Errorf("%w: execution idempotency key", ErrMailboxExchangeInvalid)
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
	selection, err := validateMailboxExecutionSelection(input.Operation, input.Selection)
	if err != nil {
		return validatedMailboxExchangeCreate{}, err
	}
	selectionState, err := validateMailboxExecutionSelectionState(input.Operation, selection, input.SelectionState)
	if err != nil {
		return validatedMailboxExchangeCreate{}, err
	}
	return validatedMailboxExchangeCreate{MailboxExchangeCreate: MailboxExchangeCreate{MailboxID: ref.MailboxID, RequestID: ref.ClientRequestID, Operation: input.Operation, Controller: controller, IdempotencyKey: input.IdempotencyKey, ExecutionIdempotencyKey: executionKey, RequestHash: hash, CanonicalPayload: append([]byte(nil), input.CanonicalPayload...), ResourceID: input.ResourceID, Selection: selection, SelectionState: selectionState}}, nil
}

func validateMailboxExecutionSelection(operation string, input *MailboxExecutionSelection) (*MailboxExecutionSelection, error) {
	if input == nil {
		return nil, nil
	}
	if operation != "create_session" && operation != "run" {
		return nil, fmt.Errorf("%w: mailbox execution selection is only valid for new work", ErrMailboxExchangeInvalid)
	}
	if !validMailboxSelectionName(input.ContextName) || !validMailboxSelectionName(input.Environment) ||
		!validMailboxSelectionName(input.Target.Profile()) {
		return nil, fmt.Errorf("%w: mailbox execution selection identity", ErrMailboxExchangeInvalid)
	}
	target, err := domain.NewExecutionTarget(input.Target.Kind(), input.Target.Profile())
	if err != nil {
		return nil, fmt.Errorf("%w: mailbox execution selection target", ErrMailboxExchangeInvalid)
	}
	if input.Source != MailboxExecutionSelectionInboxDefault && input.Source != MailboxExecutionSelectionRequestOverride {
		return nil, fmt.Errorf("%w: mailbox execution selection source", ErrMailboxExchangeInvalid)
	}
	aliases := make([]string, 0, len(input.RepositoryAliases))
	seen := make(map[string]struct{}, len(input.RepositoryAliases))
	for _, alias := range input.RepositoryAliases {
		if !validMailboxSelectionName(alias) {
			return nil, fmt.Errorf("%w: mailbox repository alias", ErrMailboxExchangeInvalid)
		}
		if _, exists := seen[alias]; exists {
			return nil, fmt.Errorf("%w: duplicate mailbox repository alias", ErrMailboxExchangeInvalid)
		}
		seen[alias] = struct{}{}
		aliases = append(aliases, alias)
	}
	if input.RepositoryAlias != "" {
		if !validMailboxSelectionName(input.RepositoryAlias) {
			return nil, fmt.Errorf("%w: selected mailbox repository alias", ErrMailboxExchangeInvalid)
		}
		if _, exists := seen[input.RepositoryAlias]; !exists {
			return nil, fmt.Errorf("%w: selected repository alias is outside mailbox scope", ErrMailboxExchangeInvalid)
		}
	}
	return &MailboxExecutionSelection{
		ContextName: input.ContextName, Environment: input.Environment, Target: target,
		Source: input.Source, RepositoryAlias: input.RepositoryAlias, RepositoryAliases: aliases,
	}, nil
}

func validateMailboxExecutionSelectionState(operation string, selection *MailboxExecutionSelection, state MailboxExecutionSelectionState) (MailboxExecutionSelectionState, error) {
	if state == "" {
		if selection != nil {
			state = MailboxExecutionSelectionResolved
		} else {
			// Keep the pre-P153 store API compatible. The mailbox processor always
			// supplies an explicit state for new P153 work.
			state = MailboxExecutionSelectionLegacy
		}
	}
	newWork := operation == "create_session" || operation == "run"
	switch state {
	case MailboxExecutionSelectionLegacy:
		if selection != nil {
			return "", fmt.Errorf("%w: legacy mailbox selection must be absent", ErrMailboxExchangeInvalid)
		}
	case MailboxExecutionSelectionResolved:
		if !newWork || selection == nil {
			return "", fmt.Errorf("%w: resolved mailbox selection requires new work", ErrMailboxExchangeInvalid)
		}
	case MailboxExecutionSelectionRejected:
		if !newWork || selection != nil {
			return "", fmt.Errorf("%w: rejected mailbox selection requires selection-free new work", ErrMailboxExchangeInvalid)
		}
	default:
		return "", fmt.Errorf("%w: mailbox selection state", ErrMailboxExchangeInvalid)
	}
	return state, nil
}

func validMailboxSelectionName(value string) bool {
	if len(value) == 0 || len(value) > 63 {
		return false
	}
	for index, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9' && index > 0) || (character == '-' && index > 0) {
			continue
		}
		return false
	}
	return true
}

func cloneMailboxExecutionSelection(input *MailboxExecutionSelection) *MailboxExecutionSelection {
	if input == nil {
		return nil
	}
	clone := *input
	clone.RepositoryAliases = append([]string(nil), input.RepositoryAliases...)
	return &clone
}

type validatedMailboxExchangeCreate struct {
	MailboxExchangeCreate
	State                    MailboxExchangeState
	ResponseRevision         int64
	ResponseBytes            []byte
	ResponseSHA256           []byte
	TerminalResponseBytes    []byte
	TerminalResponseSHA256   []byte
	AvailableEventSequence   *int64
	IdempotencyKeyExpiresAt  *time.Time
	IdempotencyBindingActive bool
	DeduplicationWarning     bool
	CreatedAt                time.Time
	UpdatedAt                time.Time
}

func (input validatedMailboxExchangeCreate) MailboxExchangeRef() MailboxExchangeRef {
	return MailboxExchangeRef{MailboxID: input.MailboxID, ClientRequestID: input.RequestID}
}

func validateMailboxRequestID(requestID string) error {
	if requestID == "" || len(requestID) > 256 || strings.IndexByte(requestID, 0) >= 0 {
		return fmt.Errorf("%w: request ID must be 1..256 bytes and contain no NUL", ErrMailboxExchangeInvalid)
	}
	return nil
}

func sameMailboxBinding(existing MailboxExchangeRecord, input validatedMailboxExchangeCreate) bool {
	return existing.MailboxID == input.MailboxID && existing.Operation == input.Operation && existing.Controller.Type() == input.Controller.Type() && existing.Controller.ID() == input.Controller.ID() && existing.IdempotencyKey == input.IdempotencyKey && domain.CompareIdempotency(existing.RequestHash, input.RequestHash) == domain.IdempotencySamePayload && bytesEqual(existing.CanonicalPayload, input.CanonicalPayload)
}

func insertMailboxExchangeOnConnection(ctx context.Context, connection *sql.Conn, input validatedMailboxExchangeCreate) error {
	selectionValues, err := mailboxExecutionSelectionValues(input.Operation, input.Selection)
	if err != nil {
		return err
	}
	_, err = connection.ExecContext(ctx, `
	INSERT INTO mailbox_exchanges (
	    exchange_id, mailbox_id, client_request_id, operation, controller_type, controller_id, client_idempotency_key, execution_idempotency_key,
	    canonical_hash_version, canonical_hash, canonical_payload, resource_id,
	    resolved_execution_context, resolved_environment, resolved_target_kind, resolved_target_profile,
	    execution_selection_source, execution_selection_state, repository_alias, repository_aliases_json,
    request_state, response_revision, terminal_response_bytes,
    terminal_response_sha256, available_event_sequence, response_bytes,
    response_sha256, response_cleanup_at, response_cleanup_started_at,
    response_file_removed_at, idempotency_key_expires_at,
    idempotency_binding_active, deduplication_warning, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, mailboxExchangeID(input.MailboxExchangeRef()), input.MailboxID, input.RequestID, input.Operation, string(input.Controller.Type()), string(input.Controller.ID()), input.IdempotencyKey, input.ExecutionIdempotencyKey,
		input.RequestHash.Version(), input.RequestHash.SHA256(), input.CanonicalPayload, input.ResourceID,
		selectionValues.contextName, selectionValues.environment, selectionValues.targetKind, selectionValues.targetProfile,
		selectionValues.source, string(input.SelectionState), selectionValues.repositoryAlias, selectionValues.repositoryAliasesJSON,
		string(input.State), input.ResponseRevision,
		nullableBytes(input.TerminalResponseBytes), nullableBytes(input.TerminalResponseSHA256), input.AvailableEventSequence,
		nullableBytes(input.ResponseBytes), nullableBytes(input.ResponseSHA256), nil, nil, nil,
		storedTimePointer(input.IdempotencyKeyExpiresAt), input.IdempotencyBindingActive, input.DeduplicationWarning,
		formatStoredTime(input.CreatedAt), formatStoredTime(input.UpdatedAt))
	if err != nil {
		return fmt.Errorf("insert mailbox exchange: %w", err)
	}
	return nil
}

type mailboxExecutionSelectionSQLValues struct {
	contextName           string
	environment           string
	targetKind            string
	targetProfile         string
	source                string
	repositoryAlias       string
	repositoryAliasesJSON string
}

func mailboxExecutionSelectionValues(operation string, selection *MailboxExecutionSelection) (mailboxExecutionSelectionSQLValues, error) {
	if selection == nil {
		return mailboxExecutionSelectionSQLValues{repositoryAliasesJSON: "[]"}, nil
	}
	validated, err := validateMailboxExecutionSelection(operation, selection)
	if err != nil {
		return mailboxExecutionSelectionSQLValues{}, err
	}
	aliases, err := json.Marshal(validated.RepositoryAliases)
	if err != nil {
		return mailboxExecutionSelectionSQLValues{}, fmt.Errorf("%w: encode mailbox repository aliases", ErrMailboxExchangeInvalid)
	}
	return mailboxExecutionSelectionSQLValues{
		contextName:           validated.ContextName,
		environment:           validated.Environment,
		targetKind:            string(validated.Target.Kind()),
		targetProfile:         validated.Target.Profile(),
		source:                validated.Source,
		repositoryAlias:       validated.RepositoryAlias,
		repositoryAliasesJSON: string(aliases),
	}, nil
}

func mailboxExecutionSelectionFromStorage(operation, contextName, environment, targetKind, targetProfile, source, repositoryAlias, repositoryAliasesJSON string) (*MailboxExecutionSelection, error) {
	hasSelection := contextName != "" || environment != "" || targetKind != "" || targetProfile != "" || source != ""
	var aliases []string
	if err := json.Unmarshal([]byte(repositoryAliasesJSON), &aliases); err != nil || aliases == nil {
		return nil, fmt.Errorf("%w: mailbox repository aliases", ErrMailboxExchangeInvalid)
	}
	if !hasSelection {
		if repositoryAlias != "" || len(aliases) != 0 {
			return nil, fmt.Errorf("%w: incomplete mailbox execution selection", ErrMailboxExchangeInvalid)
		}
		return nil, nil
	}
	target, err := domain.NewExecutionTarget(domain.TargetKind(targetKind), targetProfile)
	if err != nil {
		return nil, fmt.Errorf("%w: mailbox execution selection target", ErrMailboxExchangeInvalid)
	}
	selection, err := validateMailboxExecutionSelection(operation, &MailboxExecutionSelection{
		ContextName:       contextName,
		Environment:       environment,
		Target:            target,
		Source:            source,
		RepositoryAlias:   repositoryAlias,
		RepositoryAliases: aliases,
	})
	if err != nil {
		return nil, err
	}
	return selection, nil
}

func storedTimePointer(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatStoredTime(*value)
}

func readMailboxExchangeByIDOnConnection(ctx context.Context, connection *sql.Conn, ref MailboxExchangeRef) (MailboxExchangeRecord, bool, error) {
	validated, err := validateMailboxExchangeRef(ref)
	if err != nil {
		return MailboxExchangeRecord{}, false, err
	}
	var exists int
	if err := connection.QueryRowContext(ctx, `SELECT count(*) FROM mailbox_exchanges WHERE exchange_id = ?`, mailboxExchangeID(validated)).Scan(&exists); err != nil {
		return MailboxExchangeRecord{}, false, fmt.Errorf("lookup mailbox request ID: %w", err)
	}
	if exists == 0 {
		return MailboxExchangeRecord{}, false, nil
	}
	record, err := readMailboxExchangeOnConnection(ctx, connection, validated)
	return record, true, err
}

func readLatestMailboxExchangeByKeyOnConnection(ctx context.Context, connection *sql.Conn, mailboxID string, controller domain.ControllerIdentity, operation, key string) (MailboxExchangeRecord, bool, error) {
	if err := validateMailboxID(mailboxID); err != nil {
		return MailboxExchangeRecord{}, false, err
	}
	var clientRequestID string
	err := connection.QueryRowContext(ctx, `
SELECT client_request_id FROM mailbox_exchanges
WHERE mailbox_id = ? AND controller_type = ? AND controller_id = ? AND operation = ? AND client_idempotency_key = ?
  AND idempotency_binding_active = 1
ORDER BY created_at DESC, exchange_id DESC LIMIT 1
`, mailboxID, string(controller.Type()), string(controller.ID()), operation, key).Scan(&clientRequestID)
	if errors.Is(err, sql.ErrNoRows) {
		return MailboxExchangeRecord{}, false, nil
	}
	if err != nil {
		return MailboxExchangeRecord{}, false, fmt.Errorf("lookup mailbox idempotency key: %w", err)
	}
	record, err := readMailboxExchangeOnConnection(ctx, connection, MailboxExchangeRef{MailboxID: mailboxID, ClientRequestID: clientRequestID})
	return record, true, err
}

func readMailboxExchangeOnConnection(ctx context.Context, connection *sql.Conn, ref MailboxExchangeRef) (MailboxExchangeRecord, error) {
	validated, err := validateMailboxExchangeRef(ref)
	if err != nil {
		return MailboxExchangeRecord{}, err
	}
	var record MailboxExchangeRecord
	var controllerType, controllerID, operation, key, executionKey, payload, resourceID, state, createdAt, updatedAt string
	var selectionContext, selectionEnvironment, selectionTargetKind, selectionTargetProfile, selectionSource, selectionState, selectionRepositoryAlias, selectionRepositoryAliasesJSON string
	var version int
	var digest []byte
	var terminalBytes, terminalHash, responseBytes, responseHash []byte
	var availableCursor sql.NullInt64
	var acknowledgedAt, responseCleanupAt, responseCleanupStartedAt, responseFileRemovedAt, idempotencyKeyExpiresAt sql.NullString
	var eventFileCommandID sql.NullString
	var idempotencyBindingActive, deduplicationWarning int
	if err := connection.QueryRowContext(ctx, `
	SELECT exchange_id, mailbox_id, client_request_id, operation, controller_type, controller_id,
         client_idempotency_key, execution_idempotency_key,
         canonical_hash_version, canonical_hash, canonical_payload, resource_id,
	       resolved_execution_context, resolved_environment, resolved_target_kind, resolved_target_profile,
	       execution_selection_source, execution_selection_state, repository_alias, repository_aliases_json,
       request_state, response_revision, terminal_response_bytes,
       terminal_response_sha256, available_event_sequence, response_bytes,
       response_sha256, acknowledged_at, response_cleanup_at,
       response_cleanup_started_at, response_file_removed_at,
       idempotency_key_expires_at, idempotency_binding_active, deduplication_warning,
       COALESCE(
          (SELECT command_id FROM mailbox_event_file_references WHERE exchange_id = mailbox_exchanges.exchange_id),
          (SELECT command_id FROM mailbox_remote_event_file_references WHERE exchange_id = mailbox_exchanges.exchange_id)
       ),
       created_at, updated_at
FROM mailbox_exchanges WHERE exchange_id = ?
	`, mailboxExchangeID(validated)).Scan(&record.ExchangeID, &record.MailboxID, &record.RequestID, &operation, &controllerType, &controllerID, &key, &executionKey, &version, &digest, &payload, &resourceID, &selectionContext, &selectionEnvironment, &selectionTargetKind, &selectionTargetProfile, &selectionSource, &selectionState, &selectionRepositoryAlias, &selectionRepositoryAliasesJSON, &state, &record.ResponseRevision, &terminalBytes, &terminalHash, &availableCursor, &responseBytes, &responseHash, &acknowledgedAt, &responseCleanupAt, &responseCleanupStartedAt, &responseFileRemovedAt, &idempotencyKeyExpiresAt, &idempotencyBindingActive, &deduplicationWarning, &eventFileCommandID, &createdAt, &updatedAt); errors.Is(err, sql.ErrNoRows) {
		return MailboxExchangeRecord{}, ErrMailboxExchangeNotFound
	} else if err != nil {
		return MailboxExchangeRecord{}, fmt.Errorf("read mailbox exchange: %w", err)
	}
	if record.ExchangeID != mailboxExchangeID(validated) || record.MailboxID != validated.MailboxID || record.RequestID != validated.ClientRequestID || !validMailboxID(record.MailboxID) {
		return MailboxExchangeRecord{}, fmt.Errorf("%w: mailbox exchange identity", ErrMailboxExchangeInvalid)
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
	selection, err := mailboxExecutionSelectionFromStorage(operation, selectionContext, selectionEnvironment, selectionTargetKind, selectionTargetProfile, selectionSource, selectionRepositoryAlias, selectionRepositoryAliasesJSON)
	if err != nil {
		return MailboxExchangeRecord{}, err
	}
	record.SelectionState, err = validateMailboxExecutionSelectionState(operation, selection, MailboxExecutionSelectionState(selectionState))
	if err != nil {
		return MailboxExchangeRecord{}, err
	}
	record.Selection = selection
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
	if responseCleanupAt.Valid {
		value, err := parseStoredTime(responseCleanupAt.String)
		if err != nil {
			return MailboxExchangeRecord{}, fmt.Errorf("%w: response_cleanup_at: %v", ErrMailboxExchangeInvalid, err)
		}
		record.ResponseCleanupAt = &value
	}
	if responseCleanupStartedAt.Valid {
		value, err := parseStoredTime(responseCleanupStartedAt.String)
		if err != nil {
			return MailboxExchangeRecord{}, fmt.Errorf("%w: response_cleanup_started_at: %v", ErrMailboxExchangeInvalid, err)
		}
		record.ResponseCleanupStartedAt = &value
	}
	if responseFileRemovedAt.Valid {
		value, err := parseStoredTime(responseFileRemovedAt.String)
		if err != nil {
			return MailboxExchangeRecord{}, fmt.Errorf("%w: response_file_removed_at: %v", ErrMailboxExchangeInvalid, err)
		}
		record.ResponseFileRemovedAt = &value
	}
	if eventFileCommandID.Valid {
		record.EventFileCommandID = eventFileCommandID.String
	}
	if record.CreatedAt, err = parseStoredTime(createdAt); err != nil {
		return MailboxExchangeRecord{}, fmt.Errorf("%w: created_at: %v", ErrMailboxExchangeInvalid, err)
	}
	if record.UpdatedAt, err = parseStoredTime(updatedAt); err != nil {
		return MailboxExchangeRecord{}, fmt.Errorf("%w: updated_at: %v", ErrMailboxExchangeInvalid, err)
	}
	if (idempotencyBindingActive != 0 && idempotencyBindingActive != 1) || (deduplicationWarning != 0 && deduplicationWarning != 1) {
		return MailboxExchangeRecord{}, fmt.Errorf("%w: mailbox idempotency flags", ErrMailboxExchangeInvalid)
	}
	if err := validateExecutionIdempotencyKey(executionKey); err != nil {
		return MailboxExchangeRecord{}, fmt.Errorf("%w: execution idempotency key", ErrMailboxExchangeInvalid)
	}
	record.IdempotencyBindingActive = idempotencyBindingActive == 1
	record.DeduplicationWarning = deduplicationWarning == 1
	if idempotencyKeyExpiresAt.Valid {
		value, err := parseStoredTime(idempotencyKeyExpiresAt.String)
		if err != nil {
			return MailboxExchangeRecord{}, fmt.Errorf("%w: idempotency_key_expires_at: %v", ErrMailboxExchangeInvalid, err)
		}
		record.IdempotencyKeyExpiresAt = &value
	} else if key != "" && record.IdempotencyBindingActive {
		// Rows written before P096 retain their original 90-day window rather
		// than starting a new one when this migration is applied.
		value := record.CreatedAt.Add(DefaultSessionIdempotencyRetention)
		record.IdempotencyKeyExpiresAt = &value
	}
	if record.ResponseCleanupAt == nil && record.ResponseRevision > 0 && (state == string(MailboxExchangeComplete) || state == string(MailboxExchangeRejected) || state == string(MailboxExchangeIndeterminate)) {
		deadline := mailboxResponseCleanupDeadline(record.UpdatedAt, record.AcknowledgedAt)
		record.ResponseCleanupAt = &deadline
	}
	record.Operation, record.Controller, record.IdempotencyKey, record.ExecutionIdempotencyKey = operation, controller, key, executionKey
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

func mailboxResponseCleanupDeadline(publishedAt time.Time, acknowledgedAt *time.Time) time.Time {
	deadline := publishedAt.Add(MailboxUnackedResponseLifetime)
	if acknowledgedAt != nil {
		ackedDeadline := acknowledgedAt.Add(MailboxAckedResponseLifetime)
		if ackedDeadline.Before(deadline) {
			deadline = ackedDeadline
		}
	}
	return deadline
}
