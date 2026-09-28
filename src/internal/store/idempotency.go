package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"

	"remote-session-runner/src/internal/domain"
)

// IdempotencyRecord is the durable identity binding for one mutation key.
// ResourceID is opaque because later mutations may bind jobs, sessions,
// commands, or lifecycle operations to the same generic table.
type IdempotencyRecord struct {
	Controller domain.ControllerIdentity
	Operation  string
	Key        string
	Hash       domain.CanonicalHash
	ResourceID string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	// DeduplicationWarning remains true on replays throughout a newly accepted
	// key window when the same scoped key had already expired.
	DeduplicationWarning bool
}

// EnsureIdempotency durably records one generic mutation binding. A retained
// same-hash record returns duplicate=true; a changed hash conflicts. This
// helper is intentionally independent of a resource table so later cancel,
// close, and job mutations can use the same key namespace in their own
// resource transaction.
func (s *AuthorityStore) EnsureIdempotency(ctx context.Context, controller domain.ControllerIdentity, operation, key string, hash domain.CanonicalHash, resourceID string, retention time.Duration) (record IdempotencyRecord, duplicate bool, err error) {
	validated, err := validateIdempotencyInput(controller, operation, key, hash, resourceID, retention)
	if err != nil {
		return IdempotencyRecord{}, false, err
	}
	now := s.now().UTC()
	record, err = withImmediateTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) (IdempotencyRecord, error) {
		existing, found, err := lookupIdempotencyOnConnection(ctx, connection, validated.Controller, validated.Operation, validated.Key, now)
		if err != nil {
			return IdempotencyRecord{}, err
		}
		if found {
			if domain.CompareIdempotency(existing.Hash, validated.Hash) == domain.IdempotencyConflict {
				return IdempotencyRecord{}, ErrIdempotencyConflict
			}
			duplicate = true
			return existing, nil
		}
		validated.CreatedAt = now
		validated.ExpiresAt = now.Add(validated.Retention)
		validated.DeduplicationWarning, err = idempotencyExpiryWarningOnConnection(ctx, connection, validated.Controller, validated.Operation, validated.Key, now)
		if err != nil {
			return IdempotencyRecord{}, err
		}
		if err := recordIdempotencyOnConnection(ctx, connection, validated); err != nil {
			return IdempotencyRecord{}, err
		}
		return validated.IdempotencyRecord, nil
	})
	if err != nil {
		return IdempotencyRecord{}, false, err
	}
	return record, duplicate, nil
}

// LookupIdempotency reads a retained generic binding. Expired mappings are
// deleted in the same immediate transaction and return found=false.
func (s *AuthorityStore) LookupIdempotency(ctx context.Context, controller domain.ControllerIdentity, operation, key string) (record IdempotencyRecord, found bool, err error) {
	validatedController, err := validateController(controller)
	if err != nil {
		return IdempotencyRecord{}, false, err
	}
	operation, key, err = validateIdempotencyOperationKey(operation, key)
	if err != nil {
		return IdempotencyRecord{}, false, err
	}
	now := s.now().UTC()
	record, err = withImmediateTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) (IdempotencyRecord, error) {
		var foundRecord bool
		record, foundRecord, err = lookupIdempotencyOnConnection(ctx, connection, validatedController, operation, key, now)
		found = foundRecord
		return record, err
	})
	if err != nil {
		return IdempotencyRecord{}, false, err
	}
	return record, found, nil
}

type validatedIdempotencyInput struct {
	IdempotencyRecord
	Retention time.Duration
}

func validateIdempotencyInput(controller domain.ControllerIdentity, operation, key string, hash domain.CanonicalHash, resourceID string, retention time.Duration) (validatedIdempotencyInput, error) {
	validatedController, err := validateController(controller)
	if err != nil {
		return validatedIdempotencyInput{}, err
	}
	operation, key, err = validateIdempotencyOperationKey(operation, key)
	if err != nil {
		return validatedIdempotencyInput{}, err
	}
	validatedHash, err := domain.NewCanonicalHash(hash.Version(), hash.SHA256())
	if err != nil {
		return validatedIdempotencyInput{}, fmt.Errorf("%w: hash: %v", ErrInvalidIdempotency, err)
	}
	if resourceID == "" || len(resourceID) > 256 || strings.IndexByte(resourceID, 0) >= 0 {
		return validatedIdempotencyInput{}, fmt.Errorf("%w: resource ID must be 1..256 bytes and contain no NUL", ErrInvalidIdempotency)
	}
	if retention == 0 {
		retention = DefaultSessionIdempotencyRetention
	}
	if retention < 0 {
		return validatedIdempotencyInput{}, fmt.Errorf("%w: retention must not be negative", ErrInvalidIdempotency)
	}
	return validatedIdempotencyInput{IdempotencyRecord: IdempotencyRecord{Controller: validatedController, Operation: operation, Key: key, Hash: validatedHash, ResourceID: resourceID}, Retention: retention}, nil
}

func validateController(controller domain.ControllerIdentity) (domain.ControllerIdentity, error) {
	id, err := domain.NewControllerID(string(controller.ID()))
	if err != nil {
		return domain.ControllerIdentity{}, fmt.Errorf("%w: controller ID: %v", ErrInvalidIdempotency, err)
	}
	validated, err := domain.NewControllerIdentity(controller.Type(), id)
	if err != nil {
		return domain.ControllerIdentity{}, fmt.Errorf("%w: controller: %v", ErrInvalidIdempotency, err)
	}
	return validated, nil
}

func validateIdempotencyOperationKey(operation, key string) (string, string, error) {
	if operation == "" || len(operation) > 128 || strings.IndexByte(operation, 0) >= 0 {
		return "", "", fmt.Errorf("%w: operation must be 1..128 bytes and contain no NUL", ErrInvalidIdempotency)
	}
	if key == "" || len(key) > 256 || strings.IndexByte(key, 0) >= 0 {
		return "", "", fmt.Errorf("%w: key must be 1..256 bytes and contain no NUL", ErrIdempotencyKey)
	}
	return operation, key, nil
}

func lookupIdempotencyOnConnection(ctx context.Context, connection *sql.Conn, controller domain.ControllerIdentity, operation, key string, now time.Time) (IdempotencyRecord, bool, error) {
	var record IdempotencyRecord
	var version int
	var digest []byte
	var createdAt, expiresAt string
	var warning bool
	err := connection.QueryRowContext(ctx, `
SELECT canonical_hash_version, canonical_hash, resource_id, created_at, expires_at, deduplication_warning
FROM exec_idempotency
WHERE controller_type = ? AND controller_id = ? AND operation = ? AND idempotency_key = ?
`, string(controller.Type()), string(controller.ID()), operation, key).Scan(&version, &digest, &record.ResourceID, &createdAt, &expiresAt, &warning)
	if errors.Is(err, sql.ErrNoRows) {
		return IdempotencyRecord{}, false, nil
	}
	if err != nil {
		return IdempotencyRecord{}, false, fmt.Errorf("lookup idempotency: %w", err)
	}
	record.Controller = controller
	record.Operation, record.Key = operation, key
	var parseErr error
	if record.CreatedAt, parseErr = parseStoredTime(createdAt); parseErr != nil {
		return IdempotencyRecord{}, false, fmt.Errorf("%w: stored created_at: %v", ErrInvalidIdempotency, parseErr)
	}
	if record.ExpiresAt, parseErr = parseStoredTime(expiresAt); parseErr != nil {
		return IdempotencyRecord{}, false, fmt.Errorf("%w: stored expires_at: %v", ErrInvalidIdempotency, parseErr)
	}
	if !now.Before(record.ExpiresAt) {
		if err := recordIdempotencyExpiryWarningOnConnection(ctx, connection, controller, operation, key, record.ExpiresAt, now); err != nil {
			return IdempotencyRecord{}, false, err
		}
		if _, err := connection.ExecContext(ctx, `
DELETE FROM exec_idempotency
WHERE controller_type = ? AND controller_id = ? AND operation = ? AND idempotency_key = ?
`, string(controller.Type()), string(controller.ID()), operation, key); err != nil {
			return IdempotencyRecord{}, false, fmt.Errorf("expire idempotency: %w", err)
		}
		return IdempotencyRecord{}, false, nil
	}
	hash, err := domain.NewCanonicalHash(uint16(version), digest)
	if err != nil {
		return IdempotencyRecord{}, false, fmt.Errorf("%w: stored hash: %v", ErrInvalidIdempotency, err)
	}
	record.Hash = hash
	record.DeduplicationWarning = warning
	if record.ResourceID == "" || len(record.ResourceID) > 256 || strings.IndexByte(record.ResourceID, 0) >= 0 {
		return IdempotencyRecord{}, false, fmt.Errorf("%w: stored resource ID", ErrInvalidIdempotency)
	}
	return record, true, nil
}

func recordIdempotencyOnConnection(ctx context.Context, connection *sql.Conn, input validatedIdempotencyInput) error {
	if _, err := connection.ExecContext(ctx, `
INSERT INTO exec_idempotency (
    controller_type, controller_id, operation, idempotency_key,
    canonical_hash_version, canonical_hash, resource_id, created_at, expires_at, deduplication_warning
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, string(input.Controller.Type()), string(input.Controller.ID()), input.Operation, input.Key,
		input.Hash.Version(), input.Hash.SHA256(), input.ResourceID, formatStoredTime(input.CreatedAt), formatStoredTime(input.ExpiresAt), input.DeduplicationWarning); err != nil {
		return fmt.Errorf("record idempotency: %w", err)
	}
	return nil
}

func idempotencyKeyFingerprint(controller domain.ControllerIdentity, operation, key string) []byte {
	hash := sha256.New()
	for _, value := range []string{string(controller.Type()), string(controller.ID()), operation, key} {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(value)))
		_, _ = hash.Write(size[:])
		_, _ = hash.Write([]byte(value))
	}
	return hash.Sum(nil)
}

// recordIdempotencyExpiryWarningOnConnection keeps only a scoped key hash for
// one metadata-retention window after expiry; it never restores the expired
// resource binding or its deduplication guarantee.
func recordIdempotencyExpiryWarningOnConnection(ctx context.Context, connection *sql.Conn, controller domain.ControllerIdentity, operation, key string, expiresAt, now time.Time) error {
	warningUntil := expiresAt.Add(DefaultMetadataRetention).UTC()
	if !warningUntil.After(now.UTC()) {
		// An expired warning window must not be recreated by a delayed lookup.
		return nil
	}
	fingerprint := idempotencyKeyFingerprint(controller, operation, key)
	var existingText string
	err := connection.QueryRowContext(ctx, `
SELECT warning_until FROM exec_idempotency_expiry_warnings
WHERE controller_type = ? AND controller_id = ? AND operation = ? AND key_fingerprint = ?
`, string(controller.Type()), string(controller.ID()), operation, fingerprint).Scan(&existingText)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := connection.ExecContext(ctx, `
INSERT INTO exec_idempotency_expiry_warnings (
    controller_type, controller_id, operation, key_fingerprint, warning_until
) VALUES (?, ?, ?, ?, ?)
`, string(controller.Type()), string(controller.ID()), operation, fingerprint, formatStoredTime(warningUntil)); err != nil {
			return fmt.Errorf("record idempotency expiry warning: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read existing idempotency expiry warning: %w", err)
	}
	existing, err := parseStoredTime(existingText)
	if err != nil {
		return fmt.Errorf("parse existing idempotency expiry warning: %w", err)
	}
	if !warningUntil.After(existing) {
		return nil
	}
	if _, err := connection.ExecContext(ctx, `
UPDATE exec_idempotency_expiry_warnings SET warning_until = ?
WHERE controller_type = ? AND controller_id = ? AND operation = ? AND key_fingerprint = ?
`, formatStoredTime(warningUntil), string(controller.Type()), string(controller.ID()), operation, fingerprint); err != nil {
		return fmt.Errorf("extend idempotency expiry warning: %w", err)
	}
	return nil
}

func idempotencyExpiryWarningOnConnection(ctx context.Context, connection *sql.Conn, controller domain.ControllerIdentity, operation, key string, now time.Time) (bool, error) {
	var warningUntilText string
	err := connection.QueryRowContext(ctx, `
SELECT warning_until FROM exec_idempotency_expiry_warnings
WHERE controller_type = ? AND controller_id = ? AND operation = ?
  AND key_fingerprint = ?
`, string(controller.Type()), string(controller.ID()), operation,
		idempotencyKeyFingerprint(controller, operation, key)).Scan(&warningUntilText)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lookup idempotency expiry warning: %w", err)
	}
	warningUntil, err := parseStoredTime(warningUntilText)
	if err != nil {
		return false, fmt.Errorf("parse idempotency expiry warning: %w", err)
	}
	return now.UTC().Before(warningUntil), nil
}

// ErrInvalidIdempotency identifies malformed generic idempotency metadata.
var ErrInvalidIdempotency = errors.New("invalid idempotency record")
