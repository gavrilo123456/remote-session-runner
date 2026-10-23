// Command mailbox-owner-closeout is a one-time QAS maintenance tool for the
// three owner-approved indeterminate requests dated 2026-10-23. It deliberately
// exposes no caller-selected request IDs, database paths, mailbox roots, or
// lane overrides.
package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/mailbox"
	"remote-session-runner/src/internal/mailboxclient"
	"remote-session-runner/src/internal/store"
)

const (
	ownerMailboxID = "slidestud-io"
	ownerIdentity  = "tomasz.walczuk"
	backupPrefix   = "owner-abandoned-closeout-20261023T"
	maxArtifact    = 1 << 20
)

type approvedRequest struct {
	RequestID string
	IntentID  domain.IntentID
	JobID     domain.JobID
	SessionID domain.SessionID
	CommandID domain.CommandID
}

var approvedRequests = []approvedRequest{
	{RequestID: "req-codex-logger-redis-build-4972-progress-20261023-11", IntentID: "intent-fd9049e22bb81c44ddbf0ad130ec1d23", JobID: "job-ee5f241f2d113896eead626b78b8d66b", SessionID: "sess-4a3952ff145acf860b35671f9d9a4c69", CommandID: "cmd-e0b8951d9e2fdca9397b03778b11b115"},
	{RequestID: "req-codex-logger-ee75-update-rules-20261023-02", IntentID: "intent-df0319409a4ae405e62f7f41656ecaa6", JobID: "job-6bcf9f3449b1776dac78198093374a73", SessionID: "sess-23996ca063f3a882fd0859184e14461d", CommandID: "cmd-ad78d44f6af813ddce9d0174ac74d8dd"},
	{RequestID: "req-codex-logger-ee75-current-control-read-20261023-01", IntentID: "intent-67ec93e3f32b93f34d85a4403cfd7aa4", JobID: "job-afe7906441c9d4afbd9f2d18442ce717", SessionID: "sess-410cff1bfbc84847c199945f9d6bf76a", CommandID: "cmd-3b9ec25bef985c0d803b8a13070b2b22"},
}

type runtimeConfig struct {
	loaded     config.Config
	namespace  config.LaneNamespace
	settings   config.MacSettings
	mailbox    config.MailboxDefinition
	configPath string
}

type fileSnapshot struct {
	Path       string `json:"path"`
	Present    bool   `json:"present"`
	Size       int64  `json:"size,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
	SourceMode uint32 `json:"source_mode,omitempty"`
}

type backupManifest struct {
	Version      int            `json:"version"`
	Operation    string         `json:"operation"`
	Host         string         `json:"host"`
	Lane         string         `json:"lane"`
	CreatedAt    time.Time      `json:"created_at"`
	ConfigSHA256 string         `json:"config_sha256"`
	Schema       int            `json:"schema"`
	DatabaseFile string         `json:"database_file"`
	DatabaseSize int64          `json:"database_size"`
	DatabaseSHA  string         `json:"database_sha256"`
	Artifacts    []fileSnapshot `json:"mailbox_artifacts"`
	Requests     []string       `json:"approved_request_ids"`
}

type requestResult struct {
	RequestID string `json:"request_id"`
	State     string `json:"state"`
	Revision  int64  `json:"response_revision"`
	Cursor    *int64 `json:"available_event_sequence"`
	SHA256    string `json:"response_sha256"`
	ClosedNow bool   `json:"closed_now,omitempty"`
	AckedNow  bool   `json:"acknowledged_now,omitempty"`
	Acked     bool   `json:"acknowledged"`
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	phase := flag.String("phase", "inspect", "inspect, backup, closeout, or publish-acks")
	backupDir := flag.String("backup-dir", "", "exact private backup directory created by the backup phase")
	flag.Parse()
	if flag.NArg() != 0 {
		fatal(fmt.Errorf("unexpected positional arguments"))
	}
	if err := run(ctx, *phase, *backupDir, os.Stdout); err != nil {
		fatal(err)
	}
}

func run(ctx context.Context, phase, backupDir string, output io.Writer) error {
	if runtime.GOOS != "darwin" {
		return errors.New("owner closeout is supported only on the Mac QAS authority")
	}
	current, err := user.Current()
	if err != nil || current.Username != ownerIdentity {
		return errors.New("owner closeout requires the configured Mac service account")
	}
	runtimeConfig, err := loadRuntimeConfig(current.HomeDir)
	if err != nil {
		return err
	}
	switch phase {
	case "inspect":
		return inspect(ctx, runtimeConfig, output)
	case "backup":
		if backupDir != "" {
			return errors.New("backup phase creates a new backup and does not accept --backup-dir")
		}
		return createBackup(ctx, runtimeConfig, output)
	case "closeout":
		if backupDir == "" {
			return errors.New("closeout requires --backup-dir from the completed backup phase")
		}
		return closeout(ctx, runtimeConfig, backupDir, output)
	case "publish-acks":
		if backupDir == "" {
			return errors.New("publish-acks requires --backup-dir from the completed backup phase")
		}
		return publishACKs(ctx, runtimeConfig, backupDir, output)
	default:
		return errors.New("phase must be inspect, backup, closeout, or publish-acks")
	}
}

func loadRuntimeConfig(home string) (runtimeConfig, error) {
	namespace, err := config.DeriveSimpleLaneNamespace(config.HostKindMac, config.ReleaseLaneQAS, "")
	if err != nil || namespace.Account != ownerIdentity || namespace.ServiceRoot != filepath.Join(home, "Library", "Application Support", "RemoteSessionRunner-qas") {
		return runtimeConfig{}, errors.New("cannot derive the fixed Mac QAS runtime namespace")
	}
	configPath := namespace.ConfigFile
	loaded, err := config.LoadFile(configPath)
	if err != nil {
		return runtimeConfig{}, fmt.Errorf("load validated QAS configuration: %w", err)
	}
	settings, ok := loaded.MacSettings()
	if !ok || loaded.Kind() != config.HostKindMac || loaded.ReleaseLane() != config.ReleaseLaneQAS || loaded.SchemaVersion() != config.VersionV3 ||
		settings.Account != ownerIdentity || settings.ServiceRoot != namespace.ServiceRoot || settings.Database != namespace.Database ||
		settings.Backups != namespace.Backups || settings.MailboxRoot != namespace.MailboxRoot || settings.APISocket != namespace.APISocket ||
		settings.LocalDSocket != namespace.LocalDSocket || !settings.MailboxIngressEnabled {
		return runtimeConfig{}, errors.New("validated configuration is not the fixed Mac QAS runtime")
	}
	mailboxDef, ok := loaded.Mailbox(ownerMailboxID)
	if !ok || mailboxDef.ID != ownerMailboxID || mailboxDef.Root != filepath.Join(home, "projects", "slidestud.io", "tmp", "mailbox-") {
		return runtimeConfig{}, errors.New("the exact slidestud-io mailbox is not assigned to the expected QAS path")
	}
	return runtimeConfig{loaded: loaded, namespace: namespace, settings: settings, mailbox: mailboxDef, configPath: configPath}, nil
}

func inspect(ctx context.Context, runtime runtimeConfig, output io.Writer) error {
	database, err := store.OpenExistingCurrentReadOnly(ctx, runtime.settings.Database)
	if err != nil {
		return fmt.Errorf("open QAS authority read-only without migration: %w", err)
	}
	defer database.Close()
	var schema int
	if err := database.QueryRowContext(ctx, "PRAGMA user_version").Scan(&schema); err != nil || schema != 40 {
		return errors.New("QAS authority schema is not the expected schema 40")
	}
	authority, err := store.NewAuthorityStore(database)
	if err != nil {
		return err
	}
	if err := requireOnlyApprovedAcceptedExchanges(ctx, database); err != nil {
		return err
	}
	if err := validateExactRequestIdentitiesReadOnly(ctx, runtime); err != nil {
		return err
	}
	results, err := summarizeRequests(ctx, authority, runtime.mailbox.ID)
	if err != nil {
		return err
	}
	return writeJSON(output, struct {
		Host         string          `json:"host"`
		Lane         string          `json:"lane"`
		ConfigSHA256 string          `json:"config_sha256"`
		Schema       int             `json:"schema"`
		Requests     []requestResult `json:"requests"`
	}{"mac-control-plane", "qas", runtime.loaded.SourceSHA256(), schema, results})
}

func createBackup(ctx context.Context, runtime runtimeConfig, output io.Writer) error {
	if active, err := launchAgentActive("com.remote-session-runner.local-qas"); err != nil || active {
		return errors.New("QAS local ingress must be stopped before creating the owner-closeout backup")
	}
	if active, err := launchAgentActive("com.remote-session-runner.locald-qas"); err != nil || active {
		return errors.New("QAS locald dispatcher must be stopped before creating the owner-closeout backup")
	}
	if err := requireOnlyApprovedAcceptedExchangesReadOnly(ctx, runtime); err != nil {
		return err
	}
	if err := validateExactRequestIdentitiesReadOnly(ctx, runtime); err != nil {
		return err
	}
	info, err := os.Lstat(runtime.settings.Backups)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 || !ownedByCurrentUser(info) {
		return errors.New("QAS backup directory is not a private owner directory")
	}
	backupDir, err := os.MkdirTemp(runtime.settings.Backups, backupPrefix+"*")
	if err != nil {
		return fmt.Errorf("create QAS owner-closeout backup directory: %w", err)
	}
	if err := os.Chmod(backupDir, 0o700); err != nil {
		return fmt.Errorf("secure owner-closeout backup directory: %w", err)
	}
	database, err := store.OpenExistingCurrentReadOnly(ctx, runtime.settings.Database)
	if err != nil {
		return fmt.Errorf("open QAS authority read-only for online backup: %w", err)
	}
	defer database.Close()
	var schema int
	if err := database.QueryRowContext(ctx, "PRAGMA user_version").Scan(&schema); err != nil || schema != 40 {
		return errors.New("QAS authority schema changed from expected schema 40")
	}
	databasePath := filepath.Join(backupDir, "authority.db")
	if err := store.CreateExistingOnlineBackup(ctx, database, databasePath); err != nil {
		return fmt.Errorf("create consistent QAS online backup: %w", err)
	}
	backupCheck, err := store.OpenExistingCurrentReadOnly(ctx, databasePath)
	if err != nil {
		return fmt.Errorf("validate consistent QAS backup without migration: %w", err)
	}
	if err := backupCheck.Close(); err != nil {
		return fmt.Errorf("close QAS backup validation handle: %w", err)
	}
	configRecord, err := copyArtifact(runtime.configPath, filepath.Join(backupDir, "config", "mac.yaml"), "config/mac.yaml")
	if err != nil {
		return err
	}
	if !configRecord.Present || configRecord.SHA256 != runtime.loaded.SourceSHA256() {
		return errors.New("copied QAS configuration does not match the loaded configuration hash")
	}
	artifacts, err := archiveMailboxInputs(runtime.mailbox.Root, filepath.Join(backupDir, "mailbox"))
	if err != nil {
		return fmt.Errorf("preserve exact mailbox inputs and evidence: %w", err)
	}
	databaseBytes, databaseSHA, err := fileDigest(databasePath)
	if err != nil {
		return err
	}
	requestIDs := make([]string, 0, len(approvedRequests))
	for _, request := range approvedRequests {
		requestIDs = append(requestIDs, request.RequestID)
	}
	manifest := backupManifest{
		Version: 1, Operation: "owner-abandoned-indeterminate-closeout", Host: "mac-control-plane", Lane: "qas",
		CreatedAt: time.Now().UTC(), ConfigSHA256: runtime.loaded.SourceSHA256(), Schema: schema,
		DatabaseFile: "authority.db", DatabaseSize: databaseBytes, DatabaseSHA: databaseSHA,
		Artifacts: artifacts, Requests: requestIDs,
	}
	if err := writePrivateJSON(filepath.Join(backupDir, "manifest.json"), manifest); err != nil {
		return fmt.Errorf("write closeout backup manifest: %w", err)
	}
	return writeJSON(output, struct {
		BackupDirectory string `json:"backup_directory"`
		ConfigSHA256    string `json:"config_sha256"`
		DatabaseSize    int64  `json:"database_size"`
		DatabaseSHA256  string `json:"database_sha256"`
		ArtifactCount   int    `json:"mailbox_artifact_count"`
	}{backupDir, runtime.loaded.SourceSHA256(), databaseBytes, databaseSHA, len(artifacts)})
}

func closeout(ctx context.Context, runtime runtimeConfig, backupDir string, output io.Writer) error {
	manifest, err := validateBackup(runtime, backupDir)
	if err != nil {
		return err
	}
	if active, err := launchAgentActive("com.remote-session-runner.local-qas"); err != nil || active {
		return errors.New("QAS local ingress must be stopped before exact owner closeout")
	}
	if active, err := launchAgentActive("com.remote-session-runner.locald-qas"); err != nil || active {
		return errors.New("QAS locald dispatcher must be stopped before exact owner closeout")
	}
	database, err := store.OpenExistingCurrent(ctx, runtime.settings.Database)
	if err != nil {
		return fmt.Errorf("open existing QAS authority without migration: %w", err)
	}
	defer database.Close()
	var schema int
	if err := database.QueryRowContext(ctx, "PRAGMA user_version").Scan(&schema); err != nil || schema != manifest.Schema || schema != 40 {
		return errors.New("live QAS authority schema does not match the verified backup")
	}
	authority, err := store.NewAuthorityStore(database)
	if err != nil {
		return err
	}
	if err := requireOnlyApprovedAcceptedExchanges(ctx, database); err != nil {
		return err
	}
	results := make([]requestResult, 0, len(approvedRequests))
	projector, err := newProjector(authority, runtime.mailbox)
	if err != nil {
		return err
	}
	client, err := mailboxclient.New(runtime.mailbox.Root)
	if err != nil {
		return err
	}
	for _, approved := range approvedRequests {
		ref, err := store.NewMailboxExchangeRef(runtime.mailbox.ID, approved.RequestID)
		if err != nil {
			return err
		}
		record, err := authority.GetMailboxExchangeInMailbox(ctx, ref)
		if err != nil {
			return err
		}
		if err := validateRequestIdentity(ctx, authority, record, approved); err != nil {
			return fmt.Errorf("fresh request identity check for %s: %w", approved.RequestID, err)
		}
		closedNow := false
		if record.State == store.MailboxExchangeAccepted {
			record, closedNow, err = authority.CloseUncertainRemoteRunAsIndeterminateInMailbox(ctx, ref,
				approved.IntentID, approved.JobID, approved.SessionID, approved.CommandID)
			if err != nil {
				return fmt.Errorf("exact owner closeout %s rejected by guarded transaction: %w", approved.RequestID, err)
			}
		} else if record.State != store.MailboxExchangeComplete && record.State != store.MailboxExchangeRejected && record.State != store.MailboxExchangeIndeterminate {
			return fmt.Errorf("request %s is in unexpected state %q", approved.RequestID, record.State)
		}
		if err := projector.Publish(ctx, approved.RequestID); err != nil {
			return fmt.Errorf("publish preserved terminal response for %s: %w", approved.RequestID, err)
		}
		response, err := client.WaitResponse(ctx, approved.RequestID)
		if err != nil {
			return fmt.Errorf("read projected terminal response for %s: %w", approved.RequestID, err)
		}
		if err := validateResponseMatchesRecord(response, record); err != nil {
			return fmt.Errorf("response verification for %s: %w", approved.RequestID, err)
		}
		if response.AvailableEventSequence != nil {
			if _, err := client.ReadEventsThroughCursor(response); err != nil {
				return fmt.Errorf("event cursor verification for %s: %w", approved.RequestID, err)
			}
		}
		results = append(results, resultFor(record, closedNow, false, false))
	}
	return writeJSON(output, struct {
		BackupDirectory string          `json:"backup_directory"`
		ConfigSHA256    string          `json:"config_sha256"`
		Requests        []requestResult `json:"requests"`
	}{backupDir, runtime.loaded.SourceSHA256(), results})
}

func publishACKs(ctx context.Context, runtime runtimeConfig, backupDir string, output io.Writer) error {
	if _, err := validateBackup(runtime, backupDir); err != nil {
		return err
	}
	if active, err := launchAgentActive("com.remote-session-runner.local-qas"); err != nil || !active {
		return errors.New("QAS local ingress must be active before ACK publication")
	}
	if active, err := launchAgentActive("com.remote-session-runner.locald-qas"); err != nil || !active {
		return errors.New("QAS locald must be active to consume exact ACKs")
	}
	database, err := store.OpenExistingCurrent(ctx, runtime.settings.Database)
	if err != nil {
		return fmt.Errorf("open existing QAS authority without migration: %w", err)
	}
	defer database.Close()
	authority, err := store.NewAuthorityStore(database)
	if err != nil {
		return err
	}
	client, err := mailboxclient.New(runtime.mailbox.Root)
	if err != nil {
		return err
	}
	results := make([]requestResult, 0, len(approvedRequests))
	for _, approved := range approvedRequests {
		ref, err := store.NewMailboxExchangeRef(runtime.mailbox.ID, approved.RequestID)
		if err != nil {
			return err
		}
		record, err := authority.GetMailboxExchangeInMailbox(ctx, ref)
		if err != nil {
			return err
		}
		if record.State != store.MailboxExchangeComplete && record.State != store.MailboxExchangeRejected && record.State != store.MailboxExchangeIndeterminate {
			return fmt.Errorf("request %s has no terminal response to acknowledge", approved.RequestID)
		}
		response, err := client.WaitResponse(ctx, approved.RequestID)
		if err != nil {
			return fmt.Errorf("read exact response before ACK for %s: %w", approved.RequestID, err)
		}
		if err := validateResponseMatchesRecord(response, record); err != nil {
			return fmt.Errorf("response/revision/cursor changed before ACK for %s: %w", approved.RequestID, err)
		}
		if response.AvailableEventSequence != nil {
			if _, err := client.ReadEventsThroughCursor(response); err != nil {
				return fmt.Errorf("available event cursor for %s is not verifiable: %w", approved.RequestID, err)
			}
		}
		ackedNow := false
		if record.AcknowledgedAt == nil {
			exists, err := matchingACKPair(runtime.mailbox.Root, response)
			if err != nil {
				return fmt.Errorf("ACK preflight for %s: %w", approved.RequestID, err)
			}
			if !exists {
				if err := client.WriteAcknowledgment(approved.RequestID, response); err != nil {
					return fmt.Errorf("publish marker-last ACK for %s: %w", approved.RequestID, err)
				}
				ackedNow = true
			}
			if err := waitForAcknowledged(ctx, authority, ref, response.ResponseRevision, response.AvailableEventSequence); err != nil {
				return fmt.Errorf("wait for durable exact ACK %s: %w", approved.RequestID, err)
			}
			record, err = authority.GetMailboxExchangeInMailbox(ctx, ref)
			if err != nil {
				return err
			}
		}
		results = append(results, resultFor(record, false, ackedNow, true))
	}
	return writeJSON(output, struct {
		MailboxID string          `json:"mailbox_id"`
		Requests  []requestResult `json:"requests"`
	}{runtime.mailbox.ID, results})
}

func validateResponseMatchesRecord(response mailboxclient.Response, record store.MailboxExchangeRecord) error {
	if response.RequestID != record.RequestID || response.Operation != record.Operation || string(record.State) != response.RequestState ||
		response.ResponseRevision != record.ResponseRevision || !sameCursor(response.AvailableEventSequence, record.AvailableEventSequence) {
		return errors.New("file response does not match durable terminal identity, revision, and event cursor")
	}
	return nil
}

func validateRequestIdentity(ctx context.Context, authority *store.AuthorityStore, record store.MailboxExchangeRecord, approved approvedRequest) error {
	if record.MailboxID != ownerMailboxID || record.RequestID != approved.RequestID || record.Operation != "run" ||
		record.ResourceID != string(approved.JobID) || record.Controller.Type() != domain.ControllerTypeLocalUser || record.Controller.ID() != ownerIdentity {
		return errors.New("mailbox exchange differs from the exact owner-approved identity")
	}
	intent, err := authority.GetLocalIntent(ctx, approved.IntentID)
	if err != nil {
		return fmt.Errorf("read exact local intent identity: %w", err)
	}
	if intent.IntentID != approved.IntentID || intent.Operation != "run" || intent.ResourceID != string(approved.JobID) ||
		intent.JobID != approved.JobID || intent.SessionID != approved.SessionID || intent.CommandID != approved.CommandID ||
		intent.Target.Kind() != domain.TargetKindRemote || intent.Target.Profile() != "sandbox-host" ||
		intent.Environment != "sandbox-host-qas" || intent.Controller.Type() != domain.ControllerTypeLocalUser || intent.Controller.ID() != ownerIdentity {
		return errors.New("local intent differs from the exact owner-approved identity")
	}
	return nil
}

func resultFor(record store.MailboxExchangeRecord, closedNow, ackedNow, acked bool) requestResult {
	return requestResult{
		RequestID: record.RequestID, State: string(record.State), Revision: record.ResponseRevision,
		Cursor: cloneCursor(record.AvailableEventSequence), SHA256: hex.EncodeToString(record.ResponseSHA256),
		ClosedNow: closedNow, AckedNow: ackedNow, Acked: acked || record.AcknowledgedAt != nil,
	}
}

func summarizeRequests(ctx context.Context, authority *store.AuthorityStore, mailboxID string) ([]requestResult, error) {
	results := make([]requestResult, 0, len(approvedRequests))
	for _, approved := range approvedRequests {
		ref, err := store.NewMailboxExchangeRef(mailboxID, approved.RequestID)
		if err != nil {
			return nil, err
		}
		record, err := authority.GetMailboxExchangeInMailbox(ctx, ref)
		if err != nil {
			return nil, fmt.Errorf("read exact request %s: %w", approved.RequestID, err)
		}
		results = append(results, resultFor(record, false, false, record.AcknowledgedAt != nil))
	}
	return results, nil
}

func requireOnlyApprovedAcceptedExchangesReadOnly(ctx context.Context, runtime runtimeConfig) error {
	database, err := store.OpenExistingCurrentReadOnly(ctx, runtime.settings.Database)
	if err != nil {
		return fmt.Errorf("open QAS authority read-only for accepted-work inventory: %w", err)
	}
	defer database.Close()
	return requireOnlyApprovedAcceptedExchanges(ctx, database)
}

func requireOnlyApprovedAcceptedExchanges(ctx context.Context, database *sql.DB) error {
	rows, err := database.QueryContext(ctx, `SELECT mailbox_id, client_request_id, operation FROM mailbox_exchanges WHERE request_state = 'accepted' ORDER BY mailbox_id, client_request_id`)
	if err != nil {
		return fmt.Errorf("inventory accepted mailbox exchanges: %w", err)
	}
	defer rows.Close()
	approved := make(map[string]struct{}, len(approvedRequests))
	for _, request := range approvedRequests {
		approved[ownerMailboxID+"\x00"+request.RequestID] = struct{}{}
	}
	for rows.Next() {
		var mailboxID, requestID, operation string
		if err := rows.Scan(&mailboxID, &requestID, &operation); err != nil {
			return err
		}
		if operation != "run" {
			return fmt.Errorf("unrelated accepted exchange %s/%s blocks owner closeout", mailboxID, requestID)
		}
		if _, ok := approved[mailboxID+"\x00"+requestID]; !ok {
			return fmt.Errorf("unapproved accepted exchange %s/%s blocks owner closeout", mailboxID, requestID)
		}
	}
	return rows.Err()
}

func validateExactRequestIdentitiesReadOnly(ctx context.Context, runtime runtimeConfig) error {
	database, err := store.OpenExistingCurrentReadOnly(ctx, runtime.settings.Database)
	if err != nil {
		return err
	}
	defer database.Close()
	authority, err := store.NewAuthorityStore(database)
	if err != nil {
		return err
	}
	for _, approved := range approvedRequests {
		ref, err := store.NewMailboxExchangeRef(ownerMailboxID, approved.RequestID)
		if err != nil {
			return err
		}
		record, err := authority.GetMailboxExchangeInMailbox(ctx, ref)
		if err != nil {
			return fmt.Errorf("read exact request %s: %w", approved.RequestID, err)
		}
		if err := validateRequestIdentity(ctx, authority, record, approved); err != nil {
			return fmt.Errorf("exact request %s: %w", approved.RequestID, err)
		}
	}
	return nil
}

func archiveMailboxInputs(root, destination string) ([]fileSnapshot, error) {
	if err := os.Mkdir(destination, 0o700); err != nil {
		return nil, err
	}
	for _, directory := range []string{"inbox", "outbox", "events", "acks", "diagnostics"} {
		info, err := os.Lstat(filepath.Join(root, directory))
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !ownedByCurrentUser(info) {
			return nil, fmt.Errorf("mailbox %s directory is missing or not owner-controlled", directory)
		}
	}
	var records []fileSnapshot
	for _, approved := range approvedRequests {
		paths := []string{
			filepath.Join("inbox", approved.RequestID+".json"),
			filepath.Join("inbox", approved.RequestID+".ready"),
			filepath.Join("outbox", approved.RequestID+".json"),
			filepath.Join("acks", approved.RequestID+".json"),
			filepath.Join("acks", approved.RequestID+".ready"),
			filepath.Join("diagnostics", approved.RequestID+".json"),
			filepath.Join("events", string(approved.CommandID)+".ndjson"),
		}
		for _, relative := range paths {
			record, err := copyArtifact(filepath.Join(root, relative), filepath.Join(destination, relative), relative)
			if err != nil {
				return nil, err
			}
			records = append(records, record)
		}
	}
	return records, nil
}

func copyArtifact(source, destination, relative string) (fileSnapshot, error) {
	info, err := os.Lstat(source)
	if errors.Is(err, os.ErrNotExist) {
		return fileSnapshot{Path: relative, Present: false}, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !ownedByCurrentUser(info) || info.Size() > maxArtifact {
		return fileSnapshot{}, fmt.Errorf("mailbox evidence %s is not a bounded owner-controlled regular file", relative)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return fileSnapshot{}, fmt.Errorf("create private artifact backup directory: %w", err)
	}
	input, err := os.OpenFile(source, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return fileSnapshot{}, fmt.Errorf("open mailbox evidence %s without following links: %w", relative, err)
	}
	defer input.Close()
	opened, err := input.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return fileSnapshot{}, fmt.Errorf("mailbox evidence %s changed during snapshot", relative)
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fileSnapshot{}, fmt.Errorf("create private copy of mailbox evidence %s: %w", relative, err)
	}
	hash := sha256.New()
	count, copyErr := io.Copy(io.MultiWriter(output, hash), input)
	syncErr := output.Sync()
	closeErr := output.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil || count != info.Size() {
		return fileSnapshot{}, fmt.Errorf("copy mailbox evidence %s failed or changed: %v", relative, errors.Join(copyErr, syncErr, closeErr))
	}
	return fileSnapshot{Path: relative, Present: true, Size: count, SHA256: hex.EncodeToString(hash.Sum(nil)), SourceMode: uint32(info.Mode().Perm())}, nil
}

func validateBackup(runtime runtimeConfig, backupDir string) (backupManifest, error) {
	clean, err := filepath.Abs(backupDir)
	if err != nil || filepath.Clean(clean) != clean || filepath.Dir(clean) != runtime.settings.Backups || !strings.HasPrefix(filepath.Base(clean), backupPrefix) {
		return backupManifest{}, errors.New("backup path is outside the fixed QAS backup directory or has an unexpected identity")
	}
	info, err := os.Lstat(clean)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 || !ownedByCurrentUser(info) {
		return backupManifest{}, errors.New("closeout backup directory is not private and owner-controlled")
	}
	manifestBytes, err := readPrivateFile(filepath.Join(clean, "manifest.json"), 1<<20)
	if err != nil {
		return backupManifest{}, fmt.Errorf("read closeout backup manifest: %w", err)
	}
	var manifest backupManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil || manifest.Version != 1 ||
		manifest.Operation != "owner-abandoned-indeterminate-closeout" || manifest.Host != "mac-control-plane" || manifest.Lane != "qas" ||
		manifest.ConfigSHA256 != runtime.loaded.SourceSHA256() || manifest.Schema != 40 || manifest.DatabaseFile != "authority.db" ||
		!sameStrings(manifest.Requests, approvedRequestIDs()) {
		return backupManifest{}, errors.New("closeout backup manifest does not match current QAS configuration and exact approved request set")
	}
	databasePath := filepath.Join(clean, manifest.DatabaseFile)
	size, hash, err := fileDigest(databasePath)
	if err != nil || size != manifest.DatabaseSize || hash != manifest.DatabaseSHA {
		return backupManifest{}, errors.New("consistent QAS database backup hash or size no longer matches manifest")
	}
	backupDB, err := store.OpenExistingCurrentReadOnly(context.Background(), databasePath)
	if err != nil {
		return backupManifest{}, errors.New("consistent QAS database backup no longer passes read-only schema validation")
	}
	backupAuthority, err := store.NewAuthorityStore(backupDB)
	if err != nil {
		_ = backupDB.Close()
		return backupManifest{}, err
	}
	if err := requireOnlyApprovedAcceptedExchanges(context.Background(), backupDB); err != nil {
		_ = backupDB.Close()
		return backupManifest{}, err
	}
	for _, approved := range approvedRequests {
		ref, refErr := store.NewMailboxExchangeRef(ownerMailboxID, approved.RequestID)
		if refErr != nil {
			_ = backupDB.Close()
			return backupManifest{}, refErr
		}
		record, readErr := backupAuthority.GetMailboxExchangeInMailbox(context.Background(), ref)
		if readErr != nil || validateRequestIdentity(context.Background(), backupAuthority, record, approved) != nil {
			_ = backupDB.Close()
			return backupManifest{}, errors.New("consistent backup does not contain each exact approved request and intent")
		}
	}
	if err := backupDB.Close(); err != nil {
		return backupManifest{}, err
	}
	configBackup := filepath.Join(clean, "config", "mac.yaml")
	configSize, configHash, err := fileDigest(configBackup)
	if err != nil || configSize == 0 || configHash != manifest.ConfigSHA256 {
		return backupManifest{}, errors.New("backed-up QAS configuration does not match its loaded hash")
	}
	configInfo, err := os.Lstat(configBackup)
	if err != nil || configInfo.Mode().Perm() != 0o600 {
		return backupManifest{}, errors.New("backed-up QAS configuration has unexpected private-copy permissions")
	}
	if err := validateArchivedArtifacts(clean, manifest.Artifacts); err != nil {
		return backupManifest{}, err
	}
	return manifest, nil
}

func validateArchivedArtifacts(backupDir string, snapshots []fileSnapshot) error {
	expected := make(map[string]fileSnapshot, len(approvedRequests)*7)
	for _, snapshot := range snapshots {
		if _, duplicate := expected[snapshot.Path]; duplicate {
			return errors.New("closeout backup manifest contains duplicate mailbox artifact paths")
		}
		expected[snapshot.Path] = snapshot
	}
	for _, approved := range approvedRequests {
		for _, relative := range []string{
			filepath.Join("inbox", approved.RequestID+".json"),
			filepath.Join("inbox", approved.RequestID+".ready"),
			filepath.Join("outbox", approved.RequestID+".json"),
			filepath.Join("acks", approved.RequestID+".json"),
			filepath.Join("acks", approved.RequestID+".ready"),
			filepath.Join("diagnostics", approved.RequestID+".json"),
			filepath.Join("events", string(approved.CommandID)+".ndjson"),
		} {
			if _, ok := expected[relative]; !ok {
				return fmt.Errorf("closeout backup manifest omits exact mailbox artifact %s", relative)
			}
		}
	}
	if len(expected) != len(approvedRequests)*7 {
		return errors.New("closeout backup manifest contains an unexpected mailbox artifact count")
	}
	for relative, snapshot := range expected {
		path := filepath.Join(backupDir, "mailbox", relative)
		if !snapshot.Present {
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("archived mailbox artifact %s should be absent", relative)
			}
			continue
		}
		size, hash, err := fileDigest(path)
		if err != nil || size != snapshot.Size || hash != snapshot.SHA256 {
			return fmt.Errorf("archived mailbox artifact %s no longer matches its manifest", relative)
		}
		info, err := os.Lstat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			return fmt.Errorf("archived mailbox artifact %s has unexpected private-copy permissions", relative)
		}
	}
	return nil
}

func newProjector(authority *store.AuthorityStore, mailboxDef config.MailboxDefinition) (mailbox.Projector, error) {
	outbox, err := mailbox.NewOutbox(mailboxDef.Root)
	if err != nil {
		return mailbox.Projector{}, err
	}
	return mailbox.Projector{MailboxID: mailboxDef.ID, Authority: authority, Outbox: outbox}, nil
}

func launchAgentActive(label string) (bool, error) {
	current, err := user.Current()
	if err != nil || current.Uid == "" {
		return false, errors.New("resolve Mac service UID")
	}
	command := exec.Command("/bin/launchctl", "print", "gui/"+current.Uid+"/"+label)
	output, err := command.CombinedOutput()
	if err == nil {
		return true, nil
	}
	text := string(output)
	if strings.Contains(text, "Could not find service") || strings.Contains(text, "No such process") {
		return false, nil
	}
	return false, fmt.Errorf("cannot prove launch agent state for %s: %s", label, strings.TrimSpace(text))
}

func matchingACKPair(root string, response mailboxclient.Response) (bool, error) {
	jsonPath := filepath.Join(root, "acks", response.RequestID+".json")
	readyPath := filepath.Join(root, "acks", response.RequestID+".ready")
	jsonBytes, jsonPresent, err := readOptionalOwnerFile(jsonPath, maxArtifact)
	if err != nil {
		return false, err
	}
	readyBytes, readyPresent, err := readOptionalOwnerFile(readyPath, 16)
	if err != nil {
		return false, err
	}
	if !jsonPresent && !readyPresent {
		return false, nil
	}
	if !jsonPresent || !readyPresent || len(readyBytes) != 0 {
		return false, errors.New("an unpaired or nonempty ACK artifact already exists; preserve and inspect it")
	}
	var ack mailboxclient.Acknowledgment
	if err := json.Unmarshal(jsonBytes, &ack); err != nil || ack.RequestID != response.RequestID ||
		ack.ResponseRevision != response.ResponseRevision || !sameCursor(ack.AvailableEventSequence, response.AvailableEventSequence) {
		return false, errors.New("existing ACK pair does not exactly match current response revision and event cursor")
	}
	return true, nil
}

func waitForAcknowledged(ctx context.Context, authority *store.AuthorityStore, ref store.MailboxExchangeRef, revision int64, cursor *int64) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		record, err := authority.GetMailboxExchangeInMailbox(ctx, ref)
		if err != nil {
			return err
		}
		if record.ResponseRevision != revision || !sameCursor(record.AvailableEventSequence, cursor) {
			return errors.New("durable response changed while waiting for ACK consumption")
		}
		if record.AcknowledgedAt != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func readOptionalOwnerFile(path string, limit int64) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !ownedByCurrentUser(info) || info.Size() > limit {
		return nil, false, errors.New("mailbox artifact is not a bounded owner-controlled regular file")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, false, errors.New("mailbox artifact changed while inspected")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, false, errors.New("mailbox artifact read failed or exceeded limit")
	}
	return data, true, nil
}

func readPrivateFile(path string, limit int64) ([]byte, error) {
	data, present, err := readOptionalOwnerFile(path, limit)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, os.ErrNotExist
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		return nil, errors.New("private backup evidence file has unexpected permissions")
	}
	return data, nil
}

func writePrivateJSON(path string, value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(encoded, '\n')); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func fileDigest(path string) (int64, string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !ownedByCurrentUser(info) {
		return 0, "", errors.New("backup artifact is not an owner-controlled regular file")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return 0, "", err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil || size != info.Size() {
		return 0, "", errors.New("backup artifact changed during digest")
	}
	return size, hex.EncodeToString(hash.Sum(nil)), nil
}

func ownedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Getuid()
}

func sameCursor(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func cloneCursor(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func approvedRequestIDs() []string {
	ids := make([]string, 0, len(approvedRequests))
	for _, request := range approvedRequests {
		ids = append(ids, request.RequestID)
	}
	return ids
}

func sameStrings(left, right []string) bool {
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

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "mailbox-owner-closeout: %v\n", err)
	os.Exit(1)
}
