package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// MailboxConfiguration is the durable identity of one Mac file-ingress root.
// It intentionally carries no client payload, controller, or secret data.
type MailboxConfiguration struct {
	ID   string
	Root string
}

type mailboxConfigurationQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// ValidateConfiguredMailboxSet rejects a configuration that would remove or
// relocate a previously activated mailbox root. The monotonic registry closes
// the gap where a file-only producer publishes marker-last work after an old
// service has been quiesced but before a replacement starts. It additionally
// rejects accepted work or a live terminal response in an absent namespace.
//
// The check is read-only and deliberately returns no mailbox ID, path, or
// request data so callers can safely use it during startup diagnostics.
func (s *AuthorityStore) ValidateConfiguredMailboxSet(ctx context.Context, configurations, legacyBaseline []MailboxConfiguration) error {
	if s == nil || s.db == nil {
		return ErrNilDatabase
	}
	if ctx == nil {
		ctx = context.Background()
	}
	normalized, err := normalizeMailboxConfigurations(configurations)
	if err != nil {
		return err
	}
	baseline, err := normalizeOptionalMailboxConfigurations(legacyBaseline)
	if err != nil {
		return err
	}
	if err := validateConfiguredMailboxSet(ctx, s.db, normalized, baseline, s.now()); err != nil {
		if IsSQLiteError(err) {
			s.storageErrors.Add(1)
		}
		return err
	}
	return nil
}

// RegisterConfiguredMailboxSet records newly activated mailbox roots after
// the same removal/relocation validation under an immediate transaction. Rows
// are never deleted or changed in this PoC; a later explicit migration is
// required before an inbox can be retired.
func (s *AuthorityStore) RegisterConfiguredMailboxSet(ctx context.Context, configurations, legacyBaseline []MailboxConfiguration) error {
	if s == nil || s.db == nil {
		return ErrNilDatabase
	}
	if ctx == nil {
		ctx = context.Background()
	}
	normalized, err := normalizeMailboxConfigurations(configurations)
	if err != nil {
		return err
	}
	baseline, err := normalizeOptionalMailboxConfigurations(legacyBaseline)
	if err != nil {
		return err
	}
	_, err = withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) (struct{}, error) {
		if err := validateConfiguredMailboxSet(ctx, connection, normalized, baseline, s.now()); err != nil {
			return struct{}{}, err
		}
		registeredAt := formatStoredTime(s.now().UTC())
		for _, configuration := range configurations {
			if _, err := connection.ExecContext(ctx, `
INSERT INTO mailbox_configuration_registry (mailbox_id, mailbox_root, registered_at)
VALUES (?, ?, ?)
ON CONFLICT(mailbox_id) DO NOTHING`, configuration.ID, configuration.Root, registeredAt); err != nil {
				return struct{}{}, fmt.Errorf("register configured mailbox set: %w", err)
			}
		}
		return struct{}{}, nil
	})
	return err
}

func normalizeMailboxConfigurations(configurations []MailboxConfiguration) (map[string]string, error) {
	if len(configurations) == 0 {
		return nil, ErrMailboxExchangeInvalid
	}
	normalized := make(map[string]string, len(configurations))
	roots := make(map[string]struct{}, len(configurations))
	for _, configuration := range configurations {
		if err := validateMailboxID(configuration.ID); err != nil || !validMailboxConfigurationRoot(configuration.Root) {
			return nil, ErrMailboxExchangeInvalid
		}
		if _, exists := normalized[configuration.ID]; exists {
			return nil, ErrMailboxExchangeInvalid
		}
		if _, exists := roots[configuration.Root]; exists {
			return nil, ErrMailboxExchangeInvalid
		}
		normalized[configuration.ID] = configuration.Root
		roots[configuration.Root] = struct{}{}
	}
	return normalized, nil
}

func normalizeOptionalMailboxConfigurations(configurations []MailboxConfiguration) (map[string]string, error) {
	if len(configurations) == 0 {
		return nil, nil
	}
	return normalizeMailboxConfigurations(configurations)
}

func validMailboxConfigurationRoot(root string) bool {
	return root != "" && len(root) <= 4096 && filepath.IsAbs(root) && filepath.Clean(root) == root && root != string(filepath.Separator) && strings.IndexByte(root, 0) < 0
}

func validateConfiguredMailboxSet(ctx context.Context, query mailboxConfigurationQuerier, configurations, legacyBaseline map[string]string, nowTime time.Time) error {
	registered, err := readRegisteredMailboxConfigurations(ctx, query)
	if err != nil {
		return err
	}
	if len(registered) == 0 {
		registered = legacyBaseline
	}
	for mailboxID, root := range registered {
		if configuredRoot, exists := configurations[mailboxID]; !exists || configuredRoot != root {
			return ErrMailboxConfigurationPending
		}
	}

	mailboxIDs := make([]string, 0, len(configurations))
	arguments := make([]any, 0, len(configurations)+1)
	for mailboxID := range configurations {
		mailboxIDs = append(mailboxIDs, mailboxID)
	}
	// Configuration.Loader returns mailbox definitions in deterministic order,
	// but store callers need the query itself to be deterministic as well.
	sort.Strings(mailboxIDs)
	placeholders := make([]string, 0, len(mailboxIDs))
	for _, mailboxID := range mailboxIDs {
		placeholders = append(placeholders, "?")
		arguments = append(arguments, mailboxID)
	}
	arguments = append(arguments, formatStoredTime(nowTime.UTC()))
	var pending int
	err = query.QueryRowContext(ctx, `
SELECT EXISTS(
  SELECT 1 FROM mailbox_exchanges
  WHERE mailbox_id NOT IN (`+strings.Join(placeholders, ",")+`)
    AND (
      request_state = 'accepted'
      OR (
        request_state IN ('complete', 'rejected', 'indeterminate')
        AND acknowledged_at IS NULL
        AND response_cleanup_started_at IS NULL
        AND response_file_removed_at IS NULL
        AND (response_cleanup_at IS NULL OR response_cleanup_at > ?)
      )
    )
)`, arguments...).Scan(&pending)
	if err != nil {
		return fmt.Errorf("validate configured mailbox set: %w", err)
	}
	if pending != 0 {
		return ErrMailboxConfigurationPending
	}
	return nil
}

func readRegisteredMailboxConfigurations(ctx context.Context, query mailboxConfigurationQuerier) (map[string]string, error) {
	var tableExists int
	if err := query.QueryRowContext(ctx, `
SELECT EXISTS(
  SELECT 1 FROM sqlite_schema
  WHERE type = 'table' AND name = 'mailbox_configuration_registry'
)`).Scan(&tableExists); err != nil {
		return nil, fmt.Errorf("inspect mailbox configuration registry: %w", err)
	}
	if tableExists == 0 {
		return nil, nil
	}
	rows, err := query.QueryContext(ctx, `
SELECT mailbox_id, mailbox_root
FROM mailbox_configuration_registry
ORDER BY mailbox_id`)
	if err != nil {
		return nil, fmt.Errorf("read mailbox configuration registry: %w", err)
	}
	defer rows.Close()
	registered := make(map[string]string)
	for rows.Next() {
		var mailboxID, root string
		if err := rows.Scan(&mailboxID, &root); err != nil {
			return nil, fmt.Errorf("scan mailbox configuration registry: %w", err)
		}
		if err := validateMailboxID(mailboxID); err != nil || !validMailboxConfigurationRoot(root) {
			return nil, ErrMailboxExchangeInvalid
		}
		if _, exists := registered[mailboxID]; exists {
			return nil, ErrMailboxExchangeInvalid
		}
		registered[mailboxID] = root
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate mailbox configuration registry: %w", err)
	}
	return registered, nil
}
