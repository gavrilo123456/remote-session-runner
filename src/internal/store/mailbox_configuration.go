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

// MailboxRetirement identifies one explicitly planned mailbox retirement. The
// root is deliberately absent: the store resolves it only from the immutable
// configuration registry, so a caller cannot redirect a retirement toward an
// arbitrary directory.
type MailboxRetirement struct {
	ID string
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

// ValidateConfiguredMailboxRetirement proves whether the supplied candidate
// can explicitly retire exactly one already-registered mailbox. It returns
// the resolved historical identity only after verifying that no accepted work
// or live, unacknowledged terminal response remains in the retiring mailbox.
// The result is intended for a caller that must later remove only that exact
// directory through a separately authorized filesystem boundary.
func (s *AuthorityStore) ValidateConfiguredMailboxRetirement(ctx context.Context, configurations, legacyBaseline []MailboxConfiguration, retirement MailboxRetirement) (MailboxConfiguration, error) {
	if s == nil || s.db == nil {
		return MailboxConfiguration{}, ErrNilDatabase
	}
	if ctx == nil {
		ctx = context.Background()
	}
	normalized, err := normalizeMailboxConfigurations(configurations)
	if err != nil {
		return MailboxConfiguration{}, err
	}
	baseline, err := normalizeOptionalMailboxConfigurations(legacyBaseline)
	if err != nil {
		return MailboxConfiguration{}, err
	}
	if err := validateMailboxRetirement(retirement); err != nil {
		return MailboxConfiguration{}, err
	}
	resolved, err := validateConfiguredMailboxRetirement(ctx, s.db, normalized, baseline, retirement, s.now())
	if err != nil && IsSQLiteError(err) {
		s.storageErrors.Add(1)
	}
	return resolved, err
}

// RegisterConfiguredMailboxSet records newly activated mailbox roots after
// the same removal/relocation validation under an immediate transaction. Rows
// are never deleted or changed in this PoC; explicit retirement is recorded
// separately by RetireConfiguredMailboxSet.
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
		if err := registerMailboxConfigurations(ctx, connection, configurations, s.now()); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, nil
	})
	return err
}

// RetireConfiguredMailboxSet records one immutable retirement and any newly
// configured mailbox roots atomically. A successful retry of the same planned
// retirement returns the original resolved identity, which makes a later
// filesystem-removal retry safe without reviving the retired configuration.
func (s *AuthorityStore) RetireConfiguredMailboxSet(ctx context.Context, configurations, legacyBaseline []MailboxConfiguration, retirement MailboxRetirement) (MailboxConfiguration, error) {
	if s == nil || s.db == nil {
		return MailboxConfiguration{}, ErrNilDatabase
	}
	if ctx == nil {
		ctx = context.Background()
	}
	normalized, err := normalizeMailboxConfigurations(configurations)
	if err != nil {
		return MailboxConfiguration{}, err
	}
	baseline, err := normalizeOptionalMailboxConfigurations(legacyBaseline)
	if err != nil {
		return MailboxConfiguration{}, err
	}
	if err := validateMailboxRetirement(retirement); err != nil {
		return MailboxConfiguration{}, err
	}
	return withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) (MailboxConfiguration, error) {
		resolved, err := validateConfiguredMailboxRetirement(ctx, connection, normalized, baseline, retirement, s.now())
		if err != nil {
			return MailboxConfiguration{}, err
		}
		if err := recordMailboxConfigurationRetirement(ctx, connection, resolved, s.now()); err != nil {
			return MailboxConfiguration{}, err
		}
		// Re-read the effective active registry after writing the retirement so
		// the candidate is checked with exactly the state that will commit.
		if err := validateConfiguredMailboxSet(ctx, connection, normalized, baseline, s.now()); err != nil {
			return MailboxConfiguration{}, err
		}
		if err := registerMailboxConfigurations(ctx, connection, configurations, s.now()); err != nil {
			return MailboxConfiguration{}, err
		}
		return resolved, nil
	})
}

func validateMailboxRetirement(retirement MailboxRetirement) error {
	if err := validateMailboxID(retirement.ID); err != nil {
		return ErrMailboxExchangeInvalid
	}
	return nil
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
	if err := validateRegisteredMailboxRoots(ctx, query, configurations, legacyBaseline); err != nil {
		return err
	}
	return validateMailboxWorkForConfiguredSet(ctx, query, configurations, nowTime)
}

func validateConfiguredMailboxRetirement(ctx context.Context, query mailboxConfigurationQuerier, configurations, legacyBaseline map[string]string, retirement MailboxRetirement, nowTime time.Time) (MailboxConfiguration, error) {
	registered, err := readRegisteredMailboxConfigurations(ctx, query)
	if err != nil {
		return MailboxConfiguration{}, err
	}
	if len(registered) == 0 {
		// Retirement relies on the exact durable registry pair and deliberately
		// cannot use a caller-supplied legacy baseline as a substitute.
		return MailboxConfiguration{}, ErrMailboxConfigurationPending
	}
	retirements, err := readMailboxConfigurationRetirements(ctx, query)
	if err != nil {
		return MailboxConfiguration{}, err
	}
	active, err := effectiveRegisteredMailboxConfigurations(registered, retirements)
	if err != nil {
		return MailboxConfiguration{}, err
	}
	root, exists := registered[retirement.ID]
	if !exists {
		return MailboxConfiguration{}, ErrMailboxConfigurationPending
	}
	resolved := MailboxConfiguration{ID: retirement.ID, Root: root}
	if err := rejectRetiredMailboxReuse(configurations, retirements); err != nil {
		return MailboxConfiguration{}, err
	}
	if _, exists := configurations[retirement.ID]; exists || mailboxConfigurationRootInSet(configurations, root) {
		return MailboxConfiguration{}, ErrMailboxConfigurationPending
	}

	if retiredRoot, alreadyRetired := retirements[retirement.ID]; alreadyRetired {
		if retiredRoot != root {
			return MailboxConfiguration{}, ErrMailboxConfigurationPending
		}
		if err := validateMailboxConfigurationsAgainstRegistry(configurations, active); err != nil {
			return MailboxConfiguration{}, err
		}
		if err := validateMailboxWorkForConfiguredSet(ctx, query, configurations, nowTime); err != nil {
			return MailboxConfiguration{}, err
		}
		return resolved, nil
	}

	// The prospective state has the requested registry pair removed from the
	// active set. All other active roots must still be retained exactly.
	delete(active, retirement.ID)
	if err := validateMailboxConfigurationsAgainstRegistry(configurations, active); err != nil {
		return MailboxConfiguration{}, err
	}
	if err := validateMailboxWorkForConfiguredSet(ctx, query, configurations, nowTime); err != nil {
		return MailboxConfiguration{}, err
	}
	return resolved, nil
}

func validateMailboxWorkForConfiguredSet(ctx context.Context, query mailboxConfigurationQuerier, configurations map[string]string, nowTime time.Time) error {
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
	err := query.QueryRowContext(ctx, `
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

// validateLegacyConfiguredMailboxSet checks schema 24 before the installer
// activation boundary. All of its mailbox work belongs to the implicit
// default inbox, so retaining that root preserves every legacy exchange. The
// caller has already verified the exact legacy schema and holds only a
// read-only connection.
func validateLegacyConfiguredMailboxSet(ctx context.Context, query mailboxConfigurationQuerier, configurations, legacyBaseline []MailboxConfiguration) error {
	normalized, err := normalizeMailboxConfigurations(configurations)
	if err != nil {
		return err
	}
	baseline, err := normalizeOptionalMailboxConfigurations(legacyBaseline)
	if err != nil {
		return err
	}
	if _, exists := baseline[DefaultMailboxID]; !exists {
		// Schema 24 has no durable namespace column: every exchange belongs to
		// the implicit default inbox. Without the caller's known default root,
		// this read-only preflight cannot prove that the candidate retains it.
		return ErrMailboxConfigurationPending
	}
	return validateRegisteredMailboxRoots(ctx, query, normalized, baseline)
}

func validateRegisteredMailboxRoots(ctx context.Context, query mailboxConfigurationQuerier, configurations, legacyBaseline map[string]string) error {
	registered, err := readRegisteredMailboxConfigurations(ctx, query)
	if err != nil {
		return err
	}
	retirements, err := readMailboxConfigurationRetirements(ctx, query)
	if err != nil {
		return err
	}
	if err := rejectRetiredMailboxReuse(configurations, retirements); err != nil {
		return err
	}
	if len(registered) == 0 {
		if len(retirements) != 0 {
			return ErrMailboxConfigurationPending
		}
		registered = legacyBaseline
	} else {
		registered, err = effectiveRegisteredMailboxConfigurations(registered, retirements)
		if err != nil {
			return err
		}
	}
	return validateMailboxConfigurationsAgainstRegistry(configurations, registered)
}

func validateMailboxConfigurationsAgainstRegistry(configurations, registered map[string]string) error {
	for mailboxID, root := range registered {
		if configuredRoot, exists := configurations[mailboxID]; !exists || configuredRoot != root {
			return ErrMailboxConfigurationPending
		}
	}
	return nil
}

func effectiveRegisteredMailboxConfigurations(registered, retirements map[string]string) (map[string]string, error) {
	active := make(map[string]string, len(registered))
	for mailboxID, root := range registered {
		active[mailboxID] = root
	}
	retiredRoots := make(map[string]struct{}, len(retirements))
	for mailboxID, root := range retirements {
		registeredRoot, exists := registered[mailboxID]
		if !exists || registeredRoot != root {
			return nil, ErrMailboxConfigurationPending
		}
		if _, exists := retiredRoots[root]; exists {
			return nil, ErrMailboxConfigurationPending
		}
		retiredRoots[root] = struct{}{}
		delete(active, mailboxID)
	}
	return active, nil
}

func rejectRetiredMailboxReuse(configurations, retirements map[string]string) error {
	for mailboxID, root := range retirements {
		if _, exists := configurations[mailboxID]; exists || mailboxConfigurationRootInSet(configurations, root) {
			return ErrMailboxConfigurationPending
		}
	}
	return nil
}

func mailboxConfigurationRootInSet(configurations map[string]string, root string) bool {
	for _, configuredRoot := range configurations {
		if configuredRoot == root {
			return true
		}
	}
	return false
}

func registerMailboxConfigurations(ctx context.Context, connection *sql.Conn, configurations []MailboxConfiguration, nowTime time.Time) error {
	registeredAt := formatStoredTime(nowTime.UTC())
	for _, configuration := range configurations {
		if _, err := connection.ExecContext(ctx, `
INSERT INTO mailbox_configuration_registry (mailbox_id, mailbox_root, registered_at)
VALUES (?, ?, ?)
ON CONFLICT(mailbox_id) DO NOTHING`, configuration.ID, configuration.Root, registeredAt); err != nil {
			return fmt.Errorf("register configured mailbox set: %w", err)
		}
	}
	return nil
}

func recordMailboxConfigurationRetirement(ctx context.Context, connection *sql.Conn, retirement MailboxConfiguration, nowTime time.Time) error {
	if _, err := connection.ExecContext(ctx, `
INSERT INTO mailbox_configuration_retirements (mailbox_id, mailbox_root, retired_at)
VALUES (?, ?, ?)
ON CONFLICT(mailbox_id) DO NOTHING`, retirement.ID, retirement.Root, formatStoredTime(nowTime.UTC())); err != nil {
		return fmt.Errorf("record mailbox configuration retirement: %w", err)
	}
	var recordedRoot string
	if err := connection.QueryRowContext(ctx, `
SELECT mailbox_root
FROM mailbox_configuration_retirements
WHERE mailbox_id = ?`, retirement.ID).Scan(&recordedRoot); err != nil {
		return fmt.Errorf("read mailbox configuration retirement: %w", err)
	}
	if recordedRoot != retirement.Root {
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
	roots := make(map[string]struct{})
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
		if _, exists := roots[root]; exists {
			return nil, ErrMailboxExchangeInvalid
		}
		registered[mailboxID] = root
		roots[root] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate mailbox configuration registry: %w", err)
	}
	return registered, nil
}

func readMailboxConfigurationRetirements(ctx context.Context, query mailboxConfigurationQuerier) (map[string]string, error) {
	var tableExists int
	if err := query.QueryRowContext(ctx, `
SELECT EXISTS(
  SELECT 1 FROM sqlite_schema
  WHERE type = 'table' AND name = 'mailbox_configuration_retirements'
)`).Scan(&tableExists); err != nil {
		return nil, fmt.Errorf("inspect mailbox configuration retirements: %w", err)
	}
	if tableExists == 0 {
		var version int
		if err := query.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
			return nil, fmt.Errorf("read SQLite schema version for mailbox retirements: %w", err)
		}
		if version >= 36 {
			return nil, ErrSchemaHistory
		}
		return make(map[string]string), nil
	}
	rows, err := query.QueryContext(ctx, `
SELECT mailbox_id, mailbox_root
FROM mailbox_configuration_retirements
ORDER BY mailbox_id`)
	if err != nil {
		return nil, fmt.Errorf("read mailbox configuration retirements: %w", err)
	}
	defer rows.Close()
	retirements := make(map[string]string)
	roots := make(map[string]struct{})
	for rows.Next() {
		var mailboxID, root string
		if err := rows.Scan(&mailboxID, &root); err != nil {
			return nil, fmt.Errorf("scan mailbox configuration retirement: %w", err)
		}
		if err := validateMailboxID(mailboxID); err != nil || !validMailboxConfigurationRoot(root) {
			return nil, ErrMailboxExchangeInvalid
		}
		if _, exists := retirements[mailboxID]; exists {
			return nil, ErrMailboxExchangeInvalid
		}
		if _, exists := roots[root]; exists {
			return nil, ErrMailboxExchangeInvalid
		}
		retirements[mailboxID] = root
		roots[root] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate mailbox configuration retirements: %w", err)
	}
	return retirements, nil
}
