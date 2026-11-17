package laneinstall

// This incident-scoped operator records an unknown outcome. It cannot submit,
// replay, cancel, ACK, release target capacity, or assert target process exit.
import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"remote-session-runner/src/internal/buildinfo"
	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/mailbox"
	"remote-session-runner/src/internal/store"
)

const (
	gpuCloseoutRequest = "req-gpu-f07-native-cli-smoke-20261117-bq1"
	gpuCloseoutJob     = "job-8ae87493c54d3f07e63c2b0b426fc2e1"
	gpuCloseoutSession = "sess-93c11db0aa0cd05d1c2a0bc768e266cb"
	gpuCloseoutCommand = "cmd-d3b0357750b06d2caea304cc98971183"
	gpuCloseoutIntent  = "intent-c5db1e408c4fadb5135caaecd931da68"
	gpuCloseoutRoot    = "/Users/tomasz.walczuk/projects/gpu-services/runner/mailbox-"
)

var ErrGPUUncertainCloseout = errors.New("exact GPU uncertain closeout refused")

// GPUUncertainSnapshot contains metadata only. Its digest excludes service
// activity and observation time, so stopping ingress does not change consent.
type GPUUncertainSnapshot struct {
	RequestID            string                          `json:"request_id"`
	JobID                string                          `json:"job_id"`
	SessionID            string                          `json:"session_id"`
	CommandID            string                          `json:"command_id"`
	IntentID             string                          `json:"intent_id"`
	ResponseRevision     int64                           `json:"response_revision"`
	ResponseSHA256       string                          `json:"response_sha256"`
	IntentUpdatedAt      time.Time                       `json:"intent_updated_at"`
	LeaseOwner           string                          `json:"lease_owner"`
	LeaseExpiresAt       time.Time                       `json:"lease_expires_at"`
	AttemptCount         int                             `json:"attempt_count"`
	RequestSHA256        string                          `json:"request_sha256"`
	MailboxRequestHash   string                          `json:"mailbox_request_hash"`
	ExecutionRequestHash string                          `json:"execution_request_hash"`
	LifecycleSHA256      string                          `json:"lifecycle_sha256"`
	ConfigurationSHA256  string                          `json:"configuration_sha256"`
	ReleaseID            string                          `json:"release_id"`
	ReleaseSHA256        string                          `json:"release_sha256"`
	BundleSHA256         string                          `json:"bundle_sha256"`
	DatabaseIdentity     CatalogRetainedDatabaseIdentity `json:"database_identity"`
}

type GPUUncertainApproval struct {
	FormatVersion           int                  `json:"format_version"`
	Operation               string               `json:"operation"`
	Snapshot                GPUUncertainSnapshot `json:"snapshot"`
	SnapshotSHA256          string               `json:"snapshot_sha256"`
	OperatorRevision        string               `json:"operator_revision"`
	AuthorizeUnknownOutcome bool                 `json:"authorize_unknown_outcome"`
	ExpiresAt               time.Time            `json:"expires_at"`
}

type GPUUncertainCloseoutReport struct {
	FormatVersion    int                  `json:"format_version"`
	Mode             string               `json:"mode"`
	Snapshot         GPUUncertainSnapshot `json:"snapshot"`
	SnapshotSHA256   string               `json:"snapshot_sha256"`
	OperatorRevision string               `json:"operator_revision"`
	ServiceActive    bool                 `json:"service_active"`
	Applied          bool                 `json:"applied"`
	BackupDirectory  string               `json:"backup_directory,omitempty"`
	Disposition      string               `json:"disposition"`
	ResponseRevision int64                `json:"response_revision,omitempty"`
	ResponseSHA256   string               `json:"response_sha256,omitempty"`
	SafeToResume     string               `json:"safe_to_resume"`
}

type gpuCloseoutState struct {
	binding   scopedCatalogBinding
	exchange  store.MailboxExchangeRecord
	intent    store.LocalIntentRecord
	lifecycle []store.LocalIntentLifecycleRecord
}

type gpuCloseoutBackend struct {
	read           func(context.Context) (gpuCloseoutState, error)
	stopped        func(context.Context) error
	lock           func() (func(), error)
	backup         func(context.Context, GPUUncertainSnapshot) (string, error)
	close          func(context.Context, GPUUncertainSnapshot) (store.MailboxExchangeRecord, error)
	project        func(context.Context, store.MailboxExchangeRecord) error
	validateBackup func(context.Context, GPUUncertainSnapshot, string) error
	now            func() time.Time
	revision       string
}

// PlanGPUUncertainCloseout performs no backup, file publication or mutation.
func PlanGPUUncertainCloseout(ctx context.Context) (GPUUncertainCloseoutReport, error) {
	b, err := newGPUCloseoutBackend()
	if err != nil {
		return GPUUncertainCloseoutReport{}, err
	}
	return planGPUCloseout(ctx, b)
}

// ApplyGPUUncertainCloseout requires a separately reviewed owner-only approval
// and stopped ingress. It leaves the service stopped and the exchange unACKed.
func ApplyGPUUncertainCloseout(ctx context.Context, approvalPath string) (GPUUncertainCloseoutReport, error) {
	b, err := newGPUCloseoutBackend()
	if err != nil {
		return GPUUncertainCloseoutReport{}, err
	}
	approval, err := gpuReadApproval(approvalPath)
	if err != nil {
		return GPUUncertainCloseoutReport{}, err
	}
	return applyGPUCloseout(ctx, b, approval)
}

func gpuReadApproval(path string) (GPUUncertainApproval, error) {
	raw, err := gpuReadPrivate(path, 1<<20)
	if err != nil {
		return GPUUncertainApproval{}, err
	}
	var approval GPUUncertainApproval
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&approval) != nil || d.Decode(new(any)) != io.EOF {
		return GPUUncertainApproval{}, ErrGPUUncertainCloseout
	}
	return approval, nil
}

// ProjectGPUUncertainCloseout resumes only the derived-file publication after
// an interrupted exact closeout. It cannot close another intent or change a
// terminal response, and requires the original verified backup and approval.
func ProjectGPUUncertainCloseout(ctx context.Context, approvalPath, backupPath string) (GPUUncertainCloseoutReport, error) {
	b, err := newGPUCloseoutBackend()
	if err != nil {
		return GPUUncertainCloseoutReport{}, err
	}
	a, err := gpuReadApproval(approvalPath)
	if err != nil {
		return GPUUncertainCloseoutReport{}, err
	}
	return projectGPUCloseout(ctx, b, a, backupPath)
}

func planGPUCloseout(ctx context.Context, b gpuCloseoutBackend) (GPUUncertainCloseoutReport, error) {
	state, err := b.read(ctx)
	if err != nil {
		return GPUUncertainCloseoutReport{}, err
	}
	snapshot, err := gpuSnapshot(state)
	if err != nil {
		return GPUUncertainCloseoutReport{}, err
	}
	return GPUUncertainCloseoutReport{FormatVersion: 1, Mode: "plan", Snapshot: snapshot, SnapshotSHA256: gpuDigest(snapshot), OperatorRevision: b.revision,
		ServiceActive: state.binding.active, Disposition: "execution_outcome_unknown_no_closeout_performed", SafeToResume: "not_established"}, nil
}

func applyGPUCloseout(ctx context.Context, b gpuCloseoutBackend, a GPUUncertainApproval) (GPUUncertainCloseoutReport, error) {
	if err := gpuValidateApproval(b, a); err != nil {
		return GPUUncertainCloseoutReport{}, err
	}
	unlock, err := b.lock()
	if err != nil {
		return GPUUncertainCloseoutReport{}, err
	}
	defer unlock()
	if err = b.stopped(ctx); err != nil {
		return GPUUncertainCloseoutReport{}, err
	}
	plan, err := planGPUCloseout(ctx, b)
	if err != nil {
		return plan, err
	}
	if plan.ServiceActive || !reflect.DeepEqual(plan.Snapshot, a.Snapshot) || plan.SnapshotSHA256 != a.SnapshotSHA256 || b.now().Before(plan.Snapshot.LeaseExpiresAt) {
		return GPUUncertainCloseoutReport{}, ErrGPUUncertainCloseout
	}
	backup, err := b.backup(ctx, plan.Snapshot)
	if err != nil {
		return GPUUncertainCloseoutReport{}, err
	}
	fresh, err := planGPUCloseout(ctx, b)
	if err != nil || fresh.ServiceActive || fresh.SnapshotSHA256 != a.SnapshotSHA256 || b.stopped(ctx) != nil || !b.now().Before(a.ExpiresAt) {
		return GPUUncertainCloseoutReport{}, ErrGPUUncertainCloseout
	}
	terminal, err := b.close(ctx, plan.Snapshot)
	if err != nil {
		return GPUUncertainCloseoutReport{}, err
	}
	plan.Mode = "apply"
	plan.Applied = true
	plan.BackupDirectory = backup
	plan.Disposition = "owner_abandoned_execution_outcome_unknown_capacity_unchanged"
	if terminal.State != store.MailboxExchangeIndeterminate {
		plan.Disposition = "existing_terminal_outcome_preserved_capacity_unchanged"
	}
	plan.ResponseRevision = terminal.ResponseRevision
	plan.ResponseSHA256 = hex.EncodeToString(terminal.ResponseSHA256)
	if err = b.project(ctx, terminal); err != nil {
		return plan, fmt.Errorf("%w: terminal committed; projection incomplete; use --project-only with this backup and approval", ErrGPUUncertainCloseout)
	}
	return plan, nil
}

func gpuValidateApproval(b gpuCloseoutBackend, a GPUUncertainApproval) error {
	if a.FormatVersion != 1 || a.Operation != "gpu-f07-unknown-outcome-closeout" || !a.AuthorizeUnknownOutcome ||
		a.OperatorRevision != b.revision || !gpuRevision(b.revision) ||
		a.SnapshotSHA256 != gpuDigest(a.Snapshot) || !b.now().Before(a.ExpiresAt) || a.ExpiresAt.Sub(b.now()) > 4*time.Hour {
		return ErrGPUUncertainCloseout
	}
	return nil
}

func projectGPUCloseout(ctx context.Context, b gpuCloseoutBackend, a GPUUncertainApproval, backup string) (GPUUncertainCloseoutReport, error) {
	if err := gpuValidateApproval(b, a); err != nil {
		return GPUUncertainCloseoutReport{}, err
	}
	unlock, err := b.lock()
	if err != nil {
		return GPUUncertainCloseoutReport{}, err
	}
	defer unlock()
	if err = b.stopped(ctx); err != nil {
		return GPUUncertainCloseoutReport{}, err
	}
	state, err := b.read(ctx)
	if err != nil {
		return GPUUncertainCloseoutReport{}, err
	}
	if b.validateBackup == nil || b.validateBackup(ctx, a.Snapshot, backup) != nil || gpuVerifyClosed(state, a.Snapshot) != nil {
		return GPUUncertainCloseoutReport{}, ErrGPUUncertainCloseout
	}
	if err = b.project(ctx, state.exchange); err != nil {
		return GPUUncertainCloseoutReport{}, err
	}
	return GPUUncertainCloseoutReport{FormatVersion: 1, Mode: "project-only", Snapshot: a.Snapshot, SnapshotSHA256: a.SnapshotSHA256, OperatorRevision: b.revision,
		Applied: true, BackupDirectory: backup, Disposition: "existing_owner_abandoned_outcome_projected_capacity_unchanged", ResponseRevision: state.exchange.ResponseRevision,
		ResponseSHA256: hex.EncodeToString(state.exchange.ResponseSHA256), SafeToResume: "not_established"}, nil
}

func gpuSnapshot(s gpuCloseoutState) (GPUUncertainSnapshot, error) {
	e, i := s.exchange, s.intent
	if e.MailboxID != "gpu-services" || e.RequestID != gpuCloseoutRequest || e.Operation != "run" || e.State != store.MailboxExchangeAccepted ||
		e.ResponseRevision != 2 || e.AcknowledgedAt != nil || e.AvailableEventSequence != nil || len(e.TerminalResponseBytes) != 0 ||
		i.Operation != "run" || string(i.IntentID) != gpuCloseoutIntent || string(i.JobID) != gpuCloseoutJob || string(i.SessionID) != gpuCloseoutSession || string(i.CommandID) != gpuCloseoutCommand ||
		i.ResourceID != gpuCloseoutJob || i.Target.Kind() != domain.TargetKindRemote || i.Target.Profile() != "linux-host" || i.Environment != "sandbox-dev" ||
		i.Controller.Type() != domain.ControllerTypeLocalUser || string(i.Controller.ID()) != config.MacAccount || i.Controller != e.Controller ||
		i.DeliveryState != store.LocalIntentUncertain || i.Reason != "remote_transport_uncertain" || i.RemoteTerminalProofVersion != 0 ||
		i.LeaseOwner == "" || i.LeaseExpiresAt == nil || i.AttemptCount < 1 || len(s.lifecycle) == 0 ||
		s.lifecycle[len(s.lifecycle)-1].NewState != store.LocalIntentUncertain || s.lifecycle[len(s.lifecycle)-1].Reason != "remote_transport_uncertain" ||
		hex.EncodeToString(e.ResponseSHA256) != gpuHash(e.ResponseBytes) || e.ExecutionIdempotencyKey != i.IdempotencyKey {
		return GPUUncertainSnapshot{}, ErrGPUUncertainCloseout
	}
	identity := selectorReleaseIdentity(s.binding.selector)
	return GPUUncertainSnapshot{RequestID: e.RequestID, JobID: string(i.JobID), SessionID: string(i.SessionID), CommandID: string(i.CommandID), IntentID: string(i.IntentID),
		ResponseRevision: e.ResponseRevision, ResponseSHA256: gpuHash(e.ResponseBytes), IntentUpdatedAt: i.UpdatedAt, LeaseOwner: i.LeaseOwner, LeaseExpiresAt: *i.LeaseExpiresAt,
		AttemptCount: i.AttemptCount, RequestSHA256: gpuHash(i.PayloadJSON), MailboxRequestHash: e.RequestHash.String(), ExecutionRequestHash: i.RequestHash.String(), LifecycleSHA256: gpuDigest(s.lifecycle), ConfigurationSHA256: s.binding.configuration,
		ReleaseID: identity.ReleaseID, ReleaseSHA256: identity.ReleaseSHA256, BundleSHA256: identity.BundleSHA256, DatabaseIdentity: s.binding.database}, nil
}

func gpuVerifyClosed(s gpuCloseoutState, expected GPUUncertainSnapshot) error {
	e, i := s.exchange, s.intent
	if s.binding.active || e.MailboxID != "gpu-services" || e.RequestID != gpuCloseoutRequest || e.Operation != "run" || e.State != store.MailboxExchangeIndeterminate ||
		e.ResponseRevision != expected.ResponseRevision+1 || e.AcknowledgedAt != nil || e.AvailableEventSequence != nil || !bytes.Equal(e.ResponseBytes, e.TerminalResponseBytes) ||
		hex.EncodeToString(e.ResponseSHA256) != gpuHash(e.ResponseBytes) || i.DeliveryState != store.LocalIntentReconciled || i.Reason != store.MailboxOwnerAbandonedReason ||
		i.LeaseOwner != "" || i.LeaseExpiresAt != nil || i.RemoteTerminalProofVersion != 0 || i.Target.Kind() != domain.TargetKindRemote || i.Target.Profile() != "linux-host" || i.Environment != "sandbox-dev" ||
		i.Controller.Type() != domain.ControllerTypeLocalUser || string(i.Controller.ID()) != config.MacAccount || i.Controller != e.Controller || i.IdempotencyKey != e.ExecutionIdempotencyKey || len(s.lifecycle) < 2 {
		return ErrGPUUncertainCloseout
	}
	last := s.lifecycle[len(s.lifecycle)-1]
	if last.NewState != store.LocalIntentReconciled || last.Reason != store.MailboxOwnerAbandonedReason || gpuDigest(s.lifecycle[:len(s.lifecycle)-1]) != expected.LifecycleSHA256 {
		return ErrGPUUncertainCloseout
	}
	id := selectorReleaseIdentity(s.binding.selector)
	if expected.RequestID != e.RequestID || expected.JobID != string(i.JobID) || expected.SessionID != string(i.SessionID) || expected.CommandID != string(i.CommandID) || expected.IntentID != string(i.IntentID) ||
		expected.AttemptCount != i.AttemptCount || expected.RequestSHA256 != gpuHash(i.PayloadJSON) || expected.MailboxRequestHash != e.RequestHash.String() || expected.ExecutionRequestHash != i.RequestHash.String() ||
		expected.ConfigurationSHA256 != s.binding.configuration || expected.ReleaseID != id.ReleaseID || expected.ReleaseSHA256 != id.ReleaseSHA256 || expected.BundleSHA256 != id.BundleSHA256 || expected.DatabaseIdentity != s.binding.database {
		return ErrGPUUncertainCloseout
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(e.ResponseBytes, &body) != nil {
		return ErrGPUUncertainCloseout
	}
	allowed := map[string]bool{"inbox_id": true, "request_id": true, "operation": true, "request_state": true, "response_revision": true, "job_id": true, "session_id": true, "command_id": true, "delivery_state": true,
		"execution_selection_source": true, "resolved_environment": true, "resolved_execution_target": true, "idempotency_warning": true, "error": true}
	for key := range body {
		if !allowed[key] {
			return ErrGPUUncertainCloseout
		}
	}
	// Decode explicit wire names, not Go's underscore-insensitive assumptions.
	var wire struct {
		RequestID string `json:"request_id"`
		Operation string `json:"operation"`
		State     string `json:"request_state"`
		JobID     string `json:"job_id"`
		SessionID string `json:"session_id"`
		CommandID string `json:"command_id"`
		Delivery  string `json:"delivery_state"`
		Error     struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			Retryable bool   `json:"retryable"`
		} `json:"error"`
	}
	if json.Unmarshal(e.ResponseBytes, &wire) != nil || wire.RequestID != gpuCloseoutRequest || wire.Operation != "run" || wire.State != "indeterminate" || wire.JobID != gpuCloseoutJob || wire.SessionID != gpuCloseoutSession || wire.CommandID != gpuCloseoutCommand || wire.Delivery != "uncertain" ||
		wire.Error.Code != store.MailboxOwnerAbandonedCode || wire.Error.Message != store.MailboxOwnerAbandonedMessage || wire.Error.Retryable {
		return ErrGPUUncertainCloseout
	}
	return nil
}

func newGPUCloseoutBackend() (gpuCloseoutBackend, error) {
	if runtime.GOOS != "darwin" || os.Getuid() != 502 || os.Geteuid() != 502 || !retainedInventoryAccountAndPlatform("mac-control-plane", config.MacAccount) {
		return gpuCloseoutBackend{}, ErrGPUUncertainCloseout
	}
	plan, err := BuildPRDUpgradePlan("mac-control-plane")
	if err != nil {
		return gpuCloseoutBackend{}, err
	}
	service, err := NewPRDServiceController()
	if err != nil {
		return gpuCloseoutBackend{}, err
	}
	readBinding := func(ctx context.Context) (scopedCatalogBinding, error) {
		catalog := Plan{HostID: plan.HostID, Lane: config.ReleaseLanePRD, Namespace: plan.Namespace}
		root, err := DefaultCatalogRootForPlan(catalog)
		if err != nil {
			return scopedCatalogBinding{}, err
		}
		sel, err := InspectCatalogSelectorForPlan(catalog, root)
		if err != nil || sel.Release == nil {
			return scopedCatalogBinding{}, ErrGPUUncertainCloseout
		}
		id := sel.Release.Identity
		return readScopedCatalogBinding(ctx, plan, service, CatalogPRDScopedTeardownRequest{ReleaseID: id.ReleaseID, ExpectedReleaseSHA256: id.ReleaseSHA256, ExpectedBundleSHA256: id.BundleSHA256})
	}
	b := gpuCloseoutBackend{now: time.Now, revision: buildinfo.SourceRevision}
	b.read = func(ctx context.Context) (gpuCloseoutState, error) {
		binding, err := readBinding(ctx)
		if err != nil {
			return gpuCloseoutState{}, err
		}
		for _, suffix := range []string{"-wal", "-shm"} {
			if _, err := os.Lstat(plan.Namespace.Database + suffix); errors.Is(err, os.ErrNotExist) {
				continue
			}
			if _, err := retainedInventoryDatabaseIdentity(plan.Namespace.Database + suffix); err != nil {
				return gpuCloseoutState{}, err
			}
		}
		loaded, err := config.LoadFile(plan.Namespace.ConfigFile)
		if err != nil {
			return gpuCloseoutState{}, err
		}
		def, ok := loaded.Mailbox("gpu-services")
		if !ok || def.Root != gpuCloseoutRoot {
			return gpuCloseoutState{}, ErrGPUUncertainCloseout
		}
		db, err := store.OpenExistingCurrentReadOnly(ctx, plan.Namespace.Database)
		if err != nil {
			return gpuCloseoutState{}, err
		}
		defer db.Close()
		a, err := store.NewAuthorityStore(db)
		if err != nil {
			return gpuCloseoutState{}, err
		}
		ref, _ := store.NewMailboxExchangeRef("gpu-services", gpuCloseoutRequest)
		e, err := a.GetMailboxExchangeInMailbox(ctx, ref)
		if err != nil {
			return gpuCloseoutState{}, err
		}
		i, err := a.GetLocalIntentByIdempotency(ctx, "run", e.ExecutionIdempotencyKey, e.Controller)
		if err != nil {
			return gpuCloseoutState{}, err
		}
		l, err := a.ListLocalIntentLifecycle(ctx, i.IntentID)
		if err != nil {
			return gpuCloseoutState{}, err
		}
		return gpuCloseoutState{binding: binding, exchange: e, intent: i, lifecycle: l}, nil
	}
	b.stopped = func(ctx context.Context) error { return requireScopedTeardownServicesStopped(ctx, plan) }
	b.lock = func() (func(), error) {
		binding, err := readBinding(context.Background())
		if err != nil {
			return nil, err
		}
		fd, err := acquireCatalogRootPublicationLock(binding.root)
		if err != nil {
			return nil, err
		}
		life, err := acquireScopedTeardownLifecycleLock(plan.Namespace.LifecycleLock)
		if err != nil {
			releaseCatalogRootPublicationLock(fd)
			return nil, err
		}
		return func() { releasePRDUpgradeLock(life); releaseCatalogRootPublicationLock(fd) }, nil
	}
	b.backup = func(ctx context.Context, s GPUUncertainSnapshot) (string, error) { return gpuBackup(ctx, plan, s) }
	b.validateBackup = func(ctx context.Context, s GPUUncertainSnapshot, path string) error {
		return gpuValidateBackup(ctx, plan, s, path)
	}
	b.close = func(ctx context.Context, s GPUUncertainSnapshot) (store.MailboxExchangeRecord, error) {
		if _, err := retainedInventoryDatabaseIdentity(plan.Namespace.Database); err != nil {
			return store.MailboxExchangeRecord{}, err
		}
		db, err := store.OpenExistingCurrent(ctx, plan.Namespace.Database)
		if err != nil {
			return store.MailboxExchangeRecord{}, err
		}
		defer db.Close()
		a, err := store.NewAuthorityStore(db)
		if err != nil {
			return store.MailboxExchangeRecord{}, err
		}
		ref, _ := store.NewMailboxExchangeRef("gpu-services", gpuCloseoutRequest)
		e, _, err := a.CloseUncertainRemoteRunAsIndeterminateInMailbox(ctx, ref, domain.IntentID(s.IntentID), domain.JobID(gpuCloseoutJob), domain.SessionID(gpuCloseoutSession), domain.CommandID(gpuCloseoutCommand))
		return e, err
	}
	b.project = func(ctx context.Context, e store.MailboxExchangeRecord) error {
		db, err := store.OpenExistingCurrentReadOnly(ctx, plan.Namespace.Database)
		if err != nil {
			return err
		}
		defer db.Close()
		a, err := store.NewAuthorityStore(db)
		if err != nil {
			return err
		}
		out, err := mailbox.NewOutbox(gpuCloseoutRoot)
		if err != nil {
			return err
		}
		return (mailbox.Projector{MailboxID: "gpu-services", Authority: a, Outbox: out}).Publish(ctx, e.RequestID)
	}
	return b, nil
}

func gpuBackup(ctx context.Context, plan PRDUpgradePlan, s GPUUncertainSnapshot) (string, error) {
	if err := privateOwnerDirectory(plan.Namespace.Backups); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(plan.Namespace.Backups, "gpu-f07-uncertain-closeout-")
	if err != nil {
		return "", err
	}
	if os.Chmod(dir, 0o700) != nil {
		return "", ErrGPUUncertainCloseout
	}
	db, err := store.OpenExistingCurrentReadOnly(ctx, plan.Namespace.Database)
	if err != nil {
		return "", err
	}
	defer db.Close()
	path := filepath.Join(dir, "authority.db")
	if err = store.CreateExistingOnlineBackup(ctx, db, path); err != nil {
		return "", err
	}
	check, err := store.OpenExistingCurrentReadOnly(ctx, path)
	if err != nil {
		return "", err
	}
	if err = check.Close(); err != nil {
		return "", err
	}
	configBytes, err := gpuReadPrivate(plan.Namespace.ConfigFile, 1<<20)
	if err != nil || gpuHash(configBytes) != s.ConfigurationSHA256 {
		return "", ErrGPUUncertainCloseout
	}
	if err = gpuWritePrivate(filepath.Join(dir, "mac.yaml"), configBytes); err != nil {
		return "", err
	}
	manifest := gpuCloseoutBackupManifest{SnapshotSHA256: gpuDigest(s), Files: map[string]string{"mac.yaml": gpuHash(configBytes)}}
	manifest.Files["authority.db"], err = gpuFileHash(path)
	if err != nil {
		return "", err
	}
	for _, rel := range gpuArtifactPaths() {
		raw, err := gpuReadPrivate(filepath.Join(gpuCloseoutRoot, rel), 8<<20)
		if errors.Is(err, os.ErrNotExist) {
			if rel == "outbox/"+gpuCloseoutRequest+".json" {
				return "", ErrGPUUncertainCloseout
			}
			manifest.Absent = append(manifest.Absent, rel)
			continue
		}
		if err != nil {
			return "", err
		}
		if rel == "outbox/"+gpuCloseoutRequest+".json" && gpuHash(raw) != s.ResponseSHA256 {
			return "", ErrGPUUncertainCloseout
		}
		dest := filepath.Join(dir, "mailbox", rel)
		if err = os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			return "", err
		}
		if err = gpuWritePrivate(dest, raw); err != nil {
			return "", err
		}
		manifest.Files[filepath.Join("mailbox", rel)] = gpuHash(raw)
	}
	snapshotBytes, _ := json.MarshalIndent(s, "", "  ")
	if err = gpuWritePrivate(filepath.Join(dir, "snapshot.json"), snapshotBytes); err != nil {
		return "", err
	}
	manifest.Files["snapshot.json"] = gpuHash(snapshotBytes)
	manifestBytes, _ := json.MarshalIndent(manifest, "", "  ")
	if err = gpuWritePrivate(filepath.Join(dir, "manifest.json"), manifestBytes); err != nil {
		return "", err
	}
	if err = gpuValidateBackup(ctx, plan, s, dir); err != nil {
		return "", err
	}
	return dir, nil
}

type gpuCloseoutBackupManifest struct {
	SnapshotSHA256 string            `json:"snapshot_sha256"`
	Files          map[string]string `json:"file_sha256"`
	Absent         []string          `json:"absent_mailbox_artifacts"`
}

func gpuArtifactPaths() []string {
	return []string{"outbox/" + gpuCloseoutRequest + ".json", "inbox/" + gpuCloseoutRequest + ".json", "inbox/" + gpuCloseoutRequest + ".ready", "acks/" + gpuCloseoutRequest + ".json", "acks/" + gpuCloseoutRequest + ".ready", "diagnostics/" + gpuCloseoutRequest + ".json", "events/" + gpuCloseoutCommand + ".ndjson"}
}

func gpuValidateBackup(ctx context.Context, plan PRDUpgradePlan, s GPUUncertainSnapshot, dir string) error {
	if filepath.Dir(dir) != plan.Namespace.Backups || !strings.HasPrefix(filepath.Base(dir), "gpu-f07-uncertain-closeout-") || privateOwnerDirectory(dir) != nil {
		return ErrGPUUncertainCloseout
	}
	raw, err := gpuReadPrivate(filepath.Join(dir, "manifest.json"), 1<<20)
	if err != nil {
		return err
	}
	var m gpuCloseoutBackupManifest
	if json.Unmarshal(raw, &m) != nil || m.SnapshotSHA256 != gpuDigest(s) || m.Files["mac.yaml"] != s.ConfigurationSHA256 || m.Files[filepath.Join("mailbox", "outbox", gpuCloseoutRequest+".json")] != s.ResponseSHA256 {
		return ErrGPUUncertainCloseout
	}
	expected := map[string]bool{"authority.db": true, "mac.yaml": true, "snapshot.json": true}
	for _, rel := range gpuArtifactPaths() {
		expected[filepath.Join("mailbox", rel)] = true
	}
	for _, rel := range m.Absent {
		key := filepath.Join("mailbox", rel)
		if !expected[key] || m.Files[key] != "" {
			return ErrGPUUncertainCloseout
		}
		delete(expected, key)
		if _, err = os.Lstat(filepath.Join(dir, key)); !errors.Is(err, os.ErrNotExist) {
			return ErrGPUUncertainCloseout
		}
	}
	for rel, digest := range m.Files {
		if !expected[rel] {
			return ErrGPUUncertainCloseout
		}
		delete(expected, rel)
		actual, err := gpuFileHash(filepath.Join(dir, rel))
		if err != nil || actual != digest {
			return ErrGPUUncertainCloseout
		}
	}
	if len(expected) != 0 {
		return ErrGPUUncertainCloseout
	}
	snapshotRaw, err := gpuReadPrivate(filepath.Join(dir, "snapshot.json"), 1<<20)
	if err != nil {
		return err
	}
	var saved GPUUncertainSnapshot
	if json.Unmarshal(snapshotRaw, &saved) != nil || !reflect.DeepEqual(saved, s) {
		return ErrGPUUncertainCloseout
	}
	db, err := store.OpenExistingCurrentReadOnly(ctx, filepath.Join(dir, "authority.db"))
	if err != nil {
		return err
	}
	return db.Close()
}

func gpuFileHash(path string) (string, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	var s unix.Stat_t
	if unix.Fstat(fd, &s) != nil || s.Mode&unix.S_IFMT != unix.S_IFREG || s.Mode&0o777 != 0o600 || s.Uid != uint32(os.Getuid()) || s.Nlink != 1 {
		return "", ErrGPUUncertainCloseout
	}
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func gpuReadPrivate(path string, max int64) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrGPUUncertainCloseout
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	var s unix.Stat_t
	if unix.Fstat(fd, &s) != nil || s.Mode&unix.S_IFMT != unix.S_IFREG || s.Mode&0o777 != 0o600 || s.Uid != uint32(os.Getuid()) || s.Nlink != 1 || s.Size > max {
		return nil, ErrGPUUncertainCloseout
	}
	return io.ReadAll(io.LimitReader(f, max+1))
}

func gpuWritePrivate(path string, raw []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	return errors.Join(err, closeErr)
}

func privateOwnerDirectory(path string) error {
	var s unix.Stat_t
	if unix.Lstat(path, &s) != nil || s.Mode&unix.S_IFMT != unix.S_IFDIR || s.Mode&0o777 != 0o700 || s.Uid != uint32(os.Getuid()) {
		return ErrGPUUncertainCloseout
	}
	return nil
}
func gpuHash(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func gpuDigest(v any) string    { raw, _ := json.Marshal(v); return gpuHash(raw) }
func gpuRevision(value string) bool {
	if len(value) != 40 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
