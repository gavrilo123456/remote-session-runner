package laneinstall

// This coordinator moves file ingress, not SQLite authority. PRD's installed
// bytes and original configuration are retained; rollback restores the original
// directories only after the QAS consumer has stopped and its exchanges drained.
import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"gopkg.in/yaml.v3"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/mailboxfence"
	"remote-session-runner/src/internal/mailboxhandoff"
	"remote-session-runner/src/internal/store"
)

var ErrMailboxHandoff = errors.New("mailbox handoff guard refused the operation")

var handoffDirectories = []string{"inbox", "outbox", "events", "acks", "diagnostics"}

type handoffDirectory struct {
	Root   string `json:"root"`
	Name   string `json:"name"`
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

type MailboxHandoffOperation struct {
	Version              int                    `json:"version"`
	OperationID          string                 `json:"operation_id"`
	Phase                string                 `json:"phase"`
	CreatedAt            string                 `json:"created_at"`
	Owner                string                 `json:"owner"`
	Directory            string                 `json:"directory"`
	SourceConfig         string                 `json:"source_config"`
	TargetConfig         string                 `json:"target_config"`
	OriginalSourceSHA256 string                 `json:"original_source_sha256"`
	OriginalTargetSHA256 string                 `json:"original_target_sha256"`
	DisabledSourceSHA256 string                 `json:"disabled_source_sha256"`
	ReceiptPath          string                 `json:"receipt_path"`
	Receipt              mailboxhandoff.Receipt `json:"receipt"`
	Directories          []handoffDirectory     `json:"directories"`
	ReturnDirectories    []handoffDirectory     `json:"return_directories,omitempty"`
}

// These dependencies separate host observations from the filesystem
// transaction. Public entrypoints always supply canonical host checks and hold
// the real deployment locks; fixtures replace only that host route.
type mailboxHandoffTransactionDeps struct {
	sourceDatabase string
	targetDatabase string
	stopped        func(context.Context) error
	idle           func(context.Context, string, bool) error
	history        func(context.Context, string, *MailboxHandoffOperation) error
	reload         func(string) (MailboxHandoffOperation, error)
	checkpoint     func(string) error
}

func newMailboxHandoffTransactionDeps() mailboxHandoffTransactionDeps {
	prd, _ := BuildPRDUpgradePlan("mac-control-plane")
	qas, _ := BuildCatalogPlan("mac-control-plane", config.ReleaseLaneQAS)
	return mailboxHandoffTransactionDeps{sourceDatabase: prd.Namespace.Database, targetDatabase: qas.Namespace.Database,
		stopped: handoffStopped, idle: handoffIdle, history: handoffCollectHistory, reload: LoadMailboxHandoff,
		checkpoint: func(string) error { return nil }}
}

func (d mailboxHandoffTransactionDeps) valid() bool {
	return d.sourceDatabase != "" && d.targetDatabase != "" && d.stopped != nil && d.idle != nil && d.history != nil && d.reload != nil && d.checkpoint != nil
}

// PrepareMailboxHandoff produces an owner-only review package and consistent
// online database backups. It does not stop services, activate configs or touch
// mailbox artifacts. Only the canonical existing Mac PRD -> QAS route exists.
func PrepareMailboxHandoff(ctx context.Context, owner string, publishersPaused bool) (MailboxHandoffOperation, error) {
	if owner == "" || !publishersPaused {
		return MailboxHandoffOperation{}, ErrMailboxHandoff
	}
	prd, err := BuildPRDUpgradePlan("mac-control-plane")
	if err != nil || validatePRDPlan(prd) != nil {
		return MailboxHandoffOperation{}, ErrMailboxHandoff
	}
	qas, err := BuildCatalogPlan("mac-control-plane", config.ReleaseLaneQAS)
	if err != nil {
		return MailboxHandoffOperation{}, err
	}
	unlock, err := handoffLocks(false)
	if err != nil {
		return MailboxHandoffOperation{}, err
	}
	defer unlock()
	source, sourceBytes, err := handoffLoadConfig(prd.Namespace.ConfigFile)
	if err != nil {
		return MailboxHandoffOperation{}, err
	}
	target, targetBytes, err := handoffLoadConfig(qas.Namespace.ConfigFile)
	if err != nil {
		return MailboxHandoffOperation{}, err
	}
	sm, _ := source.MacSettings()
	tm, _ := target.MacSettings()
	if source.ReleaseLane() != config.ReleaseLanePRD || target.ReleaseLane() != config.ReleaseLaneQAS ||
		!sm.MailboxIngressEnabled || !tm.MailboxIngressEnabled || tm.MailboxHandoffReceipt != "" {
		return MailboxHandoffOperation{}, ErrMailboxHandoff
	}
	if err := handoffIdle(ctx, sm.Database, false); err != nil {
		return MailboxHandoffOperation{}, err
	}
	if err := handoffIdle(ctx, tm.Database, true); err != nil {
		return MailboxHandoffOperation{}, err
	}
	idBytes := make([]byte, 16)
	if _, err = rand.Read(idBytes); err != nil {
		return MailboxHandoffOperation{}, err
	}
	id := hex.EncodeToString(idBytes)
	directory := filepath.Join(sm.Backups, "mailbox-handoff-"+id)
	if err = os.Mkdir(directory, 0700); err != nil {
		return MailboxHandoffOperation{}, err
	}
	op := MailboxHandoffOperation{Version: 1, OperationID: id, Phase: "prepared", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Owner: owner, Directory: directory,
		SourceConfig: prd.Namespace.ConfigFile, TargetConfig: qas.Namespace.ConfigFile, OriginalSourceSHA256: handoffSHA(sourceBytes), OriginalTargetSHA256: handoffSHA(targetBytes),
		ReceiptPath: filepath.Join(tm.ServiceRoot, "staging", "mailbox-handoffs", id+".json")}
	for _, file := range []struct {
		name string
		data []byte
	}{{"prd-original.yaml", sourceBytes}, {"qas-original.yaml", targetBytes}} {
		if err = handoffWriteNew(filepath.Join(directory, file.name), file.data); err != nil {
			return op, err
		}
	}
	op.Receipt = mailboxhandoff.Receipt{Version: 1, OperationID: id, SourceLane: "prd", TargetLane: "qas", SourceServiceRoot: sm.ServiceRoot, TargetServiceRoot: tm.ServiceRoot,
		SourceConfigSHA256: op.OriginalSourceSHA256, Phase: "active", EnvironmentAliases: map[string]string{}}
	var sourceDoc, targetDoc map[string]any
	if yaml.Unmarshal(sourceBytes, &sourceDoc) != nil || yaml.Unmarshal(targetBytes, &targetDoc) != nil {
		return op, ErrMailboxHandoff
	}
	sourceDoc["mailboxes"] = map[string]any{}
	sourceMac := sourceDoc["mac"].(map[string]any)
	sourceMac["mailbox_ingress_enabled"] = false
	targetMac := targetDoc["mac"].(map[string]any)
	targetMac["mailbox_handoff_receipt"] = op.ReceiptPath
	targetMailboxes := targetDoc["mailboxes"].(map[string]any)
	contextNames := map[string]string{}
	for _, oldName := range source.ExecutionContextNames() {
		old, _ := source.ExecutionContext(oldName)
		for _, newName := range target.ExecutionContextNames() {
			next, _ := target.ExecutionContext(newName)
			if old.Target.Kind() == next.Target.Kind() && old.Target.Profile() == next.Target.Profile() {
				if previous, exists := op.Receipt.EnvironmentAliases[old.Environment]; exists && previous != next.Environment {
					return op, fmt.Errorf("%w: conflicting QAS environment alias", ErrMailboxHandoff)
				}
				contextNames[oldName] = newName
				op.Receipt.EnvironmentAliases[old.Environment] = next.Environment
				break
			}
		}
		if contextNames[oldName] == "" {
			return op, fmt.Errorf("%w: no matching QAS target", ErrMailboxHandoff)
		}
	}
	for _, sourceID := range source.MailboxNames() {
		definition, _ := source.Mailbox(sourceID)
		if exists, e := mailboxhandoff.MarkerExists(definition.Root); e != nil || exists {
			return op, ErrMailboxHandoff
		}
		targetID := sourceID
		if targetID == "default" {
			targetID = "prd-default"
		}
		if _, exists := targetMailboxes[targetID]; exists {
			return op, ErrMailboxHandoff
		}
		dev, ino, err := handoffIdentity(definition.Root, true)
		if err != nil {
			return op, err
		}
		entry := mailboxhandoff.Mailbox{SourceID: sourceID, TargetID: targetID, Root: definition.Root, Device: dev, Inode: ino, ArchiveRelative: filepath.Join(".runner-mailbox-handoffs", id, "prd")}
		for _, name := range handoffDirectories {
			d, i, e := handoffIdentity(filepath.Join(definition.Root, name), true)
			if e != nil {
				return op, e
			}
			op.Directories = append(op.Directories, handoffDirectory{definition.Root, name, d, i})
		}
		allowed := make([]string, 0, len(definition.AllowedExecution))
		for _, name := range definition.AllowedExecution {
			allowed = append(allowed, contextNames[name])
		}
		targetMailboxes[targetID] = map[string]any{"root": definition.Root, "repository_aliases": definition.RepositoryAliases, "default_execution": contextNames[definition.DefaultExecution], "allowed_execution": allowed, "durable_orphan_cleanup": false}
		op.Receipt.Mailboxes = append(op.Receipt.Mailboxes, entry)
	}
	disabled, err := yaml.Marshal(sourceDoc)
	if err != nil {
		return op, err
	}
	candidate, err := yaml.Marshal(targetDoc)
	if err != nil {
		return op, err
	}
	op.DisabledSourceSHA256 = handoffSHA(disabled)
	op.Receipt.TargetConfigSHA256 = handoffSHA(candidate)
	if err = handoffWriteNew(filepath.Join(directory, "prd-disabled.yaml"), disabled); err != nil {
		return op, err
	}
	if err = handoffWriteNew(filepath.Join(directory, "qas-handoff.yaml"), candidate); err != nil {
		return op, err
	}
	if _, err = config.LoadFile(filepath.Join(directory, "prd-disabled.yaml")); err != nil {
		return op, err
	}
	if _, err = config.LoadFile(filepath.Join(directory, "qas-handoff.yaml")); err != nil {
		return op, err
	}
	for _, item := range []struct{ database, name string }{{sm.Database, "prd.sqlite"}, {tm.Database, "qas.sqlite"}} {
		db, e := store.OpenExistingRestartPreflightReadOnly(ctx, item.database)
		if e != nil {
			return op, e
		}
		e = store.CreateExistingOnlineBackup(ctx, db, filepath.Join(directory, item.name))
		e = errors.Join(e, db.Close())
		if e != nil {
			return op, e
		}
	}
	if err = handoffNativeRegistry(ctx, tm.Database, target); err != nil {
		return op, err
	}
	if err = handoffSave(op); err != nil {
		return op, err
	}
	return op, nil
}

func LoadMailboxHandoff(path string) (MailboxHandoffOperation, error) {
	op, err := readMailboxHandoffJournal(path)
	if err != nil {
		return op, err
	}
	prd, e := BuildPRDUpgradePlan("mac-control-plane")
	qas, e2 := BuildCatalogPlan("mac-control-plane", config.ReleaseLaneQAS)
	if e != nil || e2 != nil || validatePRDPlan(prd) != nil || len(op.OperationID) != 32 || strings.Trim(op.OperationID, "0123456789abcdef") != "" || op.Version != 1 ||
		op.SourceConfig != prd.Namespace.ConfigFile || op.TargetConfig != qas.Namespace.ConfigFile ||
		op.Directory != filepath.Join(prd.Namespace.Backups, "mailbox-handoff-"+op.OperationID) || path != filepath.Join(op.Directory, "operation.json") ||
		op.ReceiptPath != filepath.Join(qas.Namespace.ServiceRoot, "staging", "mailbox-handoffs", op.OperationID+".json") || op.Receipt.OperationID != op.OperationID {
		return op, ErrMailboxHandoff
	}
	return op, validateMailboxHandoffOperation(op)
}

func readMailboxHandoffJournal(path string) (MailboxHandoffOperation, error) {
	var op MailboxHandoffOperation
	raw, err := handoffRead(path)
	if err != nil {
		return op, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&op) != nil {
		return op, ErrMailboxHandoff
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return op, ErrMailboxHandoff
	}
	if path != filepath.Join(op.Directory, "operation.json") {
		return op, ErrMailboxHandoff
	}
	return op, nil
}

func validateMailboxHandoffOperation(op MailboxHandoffOperation) error {
	prd, e := BuildPRDUpgradePlan("mac-control-plane")
	qas, e2 := BuildCatalogPlan("mac-control-plane", config.ReleaseLaneQAS)
	if e != nil || e2 != nil || op.Version != 1 || !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(op.OperationID) || op.Owner == "" ||
		op.Directory != filepath.Join(prd.Namespace.Backups, "mailbox-handoff-"+op.OperationID) || op.SourceConfig != prd.Namespace.ConfigFile || op.TargetConfig != qas.Namespace.ConfigFile ||
		op.ReceiptPath != filepath.Join(qas.Namespace.ServiceRoot, "staging", "mailbox-handoffs", op.OperationID+".json") ||
		op.Receipt.OperationID != op.OperationID || op.Receipt.SourceServiceRoot != prd.Namespace.ServiceRoot || op.Receipt.TargetServiceRoot != qas.Namespace.ServiceRoot ||
		op.Receipt.SourceConfigSHA256 != op.OriginalSourceSHA256 || mailboxhandoff.ValidateReceipt(op.Receipt) != nil {
		return ErrMailboxHandoff
	}
	for _, digest := range []string{op.OriginalSourceSHA256, op.OriginalTargetSHA256, op.DisabledSourceSHA256} {
		if !prdDigestPattern.MatchString(digest) {
			return ErrMailboxHandoff
		}
	}
	if op.Phase != "prepared" && op.Phase != "activating" && op.Phase != "active" && op.Phase != "returning" && op.Phase != "returned" {
		return ErrMailboxHandoff
	}
	roots := map[string]bool{}
	for _, entry := range op.Receipt.Mailboxes {
		if entry.ArchiveRelative != filepath.Join(".runner-mailbox-handoffs", op.OperationID, "prd") {
			return ErrMailboxHandoff
		}
		roots[entry.Root] = true
	}
	validateDirs := func(dirs []handoffDirectory) error {
		if len(dirs) != len(roots)*len(handoffDirectories) {
			return ErrMailboxHandoff
		}
		seen := map[string]bool{}
		for _, d := range dirs {
			allowed := false
			for _, name := range handoffDirectories {
				allowed = allowed || d.Name == name
			}
			key := d.Root + "/" + d.Name
			if !roots[d.Root] || !allowed || seen[key] || d.Inode == 0 {
				return ErrMailboxHandoff
			}
			seen[key] = true
		}
		return nil
	}
	if e = validateDirs(op.Directories); e != nil {
		return e
	}
	if len(op.ReturnDirectories) != 0 || op.Phase == "returning" || op.Phase == "returned" {
		return validateDirs(op.ReturnDirectories)
	}
	return nil
}

// Use the same catalog and lifecycle locks as deployment/selector operations.
// The order is catalog -> PRD -> QAS; no effectful phase can overlap another
// handoff or a supported install/configuration transaction.
func handoffLocks(skipQAS bool) (func(), error) {
	prd, e := BuildPRDUpgradePlan("mac-control-plane")
	if e != nil {
		return nil, e
	}
	qas, e := BuildCatalogPlan("mac-control-plane", config.ReleaseLaneQAS)
	if e != nil {
		return nil, e
	}
	root, e := DefaultCatalogRootForPlan(qas)
	if e != nil {
		return nil, e
	}
	catalogFD, e := acquireCatalogRootPublicationLock(root)
	if e != nil {
		return nil, e
	}
	fds := []int{catalogFD}
	closeAll := func() {
		for i := len(fds) - 1; i >= 0; i-- {
			_ = unix.Flock(fds[i], unix.LOCK_UN)
			_ = unix.Close(fds[i])
		}
	}
	prdFD, e := acquirePRDUpgradeLock(prd)
	if e != nil {
		closeAll()
		return nil, e
	}
	fds = append(fds, prdFD)
	if !skipQAS {
		qasFD, e := acquireSimpleLaneLock(qas)
		if e != nil {
			closeAll()
			return nil, e
		}
		fds = append(fds, qasFD)
	}
	return closeAll, nil
}

func handoffResumeConfigs(op MailboxHandoffOperation) error {
	for _, item := range []struct{ path, old, next string }{{op.SourceConfig, op.OriginalSourceSHA256, op.DisabledSourceSHA256}, {op.TargetConfig, op.OriginalTargetSHA256, op.Receipt.TargetConfigSHA256}} {
		raw, e := handoffRead(item.path)
		if e != nil {
			return e
		}
		sum := handoffSHA(raw)
		if sum != item.old && sum != item.next {
			return ErrMailboxHandoff
		}
	}
	return nil
}

// StopMailboxHandoffConsumers performs the narrowly scoped pause required by
// this operation. Terminal-lost reservations are retained; they are not jobs
// to recover or release. Queued/running/active/resumable work blocks the pause.
func StopMailboxHandoffConsumers(ctx context.Context, op MailboxHandoffOperation) error {
	if err := validateMailboxHandoffOperation(op); err != nil {
		return err
	}
	unlock, err := handoffLocks(true)
	if err != nil {
		return err
	}
	defer unlock()
	op, err = LoadMailboxHandoff(filepath.Join(op.Directory, "operation.json"))
	if err != nil {
		return err
	}
	if op.Phase == "active" {
		if err := handoffExpectedConfigs(op, true); err != nil {
			return err
		}
		prd, _ := BuildPRDUpgradePlan("mac-control-plane")
		qas, _ := BuildCatalogPlan("mac-control-plane", config.ReleaseLaneQAS)
		pc, e := NewPRDServiceController()
		if e != nil {
			return e
		}
		if e = handoffIdle(ctx, prd.Namespace.Database, false); e != nil {
			return e
		}
		if e = handoffIdle(ctx, qas.Namespace.Database, true); e != nil {
			return e
		}
		qc, e := NewSimpleServiceController()
		if e != nil {
			return e
		}
		if e = qc.Stop(ctx, qas); e != nil {
			return e
		}
		return pc.Stop(ctx, prd)
	}
	if op.Phase != "prepared" {
		return ErrMailboxHandoff
	}
	if err := handoffExpectedConfigs(op, false); err != nil {
		return err
	}
	prd, _ := BuildPRDUpgradePlan("mac-control-plane")
	qas, _ := BuildCatalogPlan("mac-control-plane", config.ReleaseLaneQAS)
	if err := handoffIdle(ctx, prd.Namespace.Database, false); err != nil {
		return err
	}
	if err := handoffIdle(ctx, qas.Namespace.Database, true); err != nil {
		return err
	}
	pc, err := NewPRDServiceController()
	if err != nil {
		return err
	}
	qc, err := NewSimpleServiceController()
	if err != nil {
		return err
	}
	if err = qc.Stop(ctx, qas); err != nil {
		return err
	}
	return pc.Stop(ctx, prd)
}

// ActivateMailboxHandoff is restartable. Each original directory is renamed,
// never copied/deleted, into its private epoch archive before new ingress is
// created. A partial operation leaves both consumers stopped and fails closed.
func ActivateMailboxHandoff(ctx context.Context, op MailboxHandoffOperation) (MailboxHandoffOperation, error) {
	if err := validateMailboxHandoffOperation(op); err != nil {
		return op, err
	}
	unlock, err := handoffLocks(false)
	if err != nil {
		return op, err
	}
	defer unlock()
	return activateMailboxHandoffLocked(ctx, op, newMailboxHandoffTransactionDeps())
}

func activateMailboxHandoffLocked(ctx context.Context, op MailboxHandoffOperation, deps mailboxHandoffTransactionDeps) (MailboxHandoffOperation, error) {
	if !deps.valid() {
		return op, ErrMailboxHandoff
	}
	op, err := deps.reload(filepath.Join(op.Directory, "operation.json"))
	if err != nil {
		return op, err
	}
	if op.Phase == "active" {
		return op, handoffExpectedConfigs(op, true)
	}
	if op.Phase != "prepared" && op.Phase != "activating" {
		return op, ErrMailboxHandoff
	}
	if err := deps.stopped(ctx); err != nil {
		return op, err
	}
	if err := handoffResumeConfigs(op); err != nil {
		return op, err
	}
	if err := deps.idle(ctx, deps.sourceDatabase, false); err != nil {
		return op, err
	}
	if err := deps.idle(ctx, deps.targetDatabase, true); err != nil {
		return op, err
	}
	if op.Phase == "prepared" {
		if err := handoffExpectedConfigs(op, false); err != nil {
			return op, err
		}
		if err := deps.idle(ctx, deps.sourceDatabase, false); err != nil {
			return op, err
		}
		if err := deps.idle(ctx, deps.targetDatabase, true); err != nil {
			return op, err
		}
	}
	var locks []*mailboxfence.Lock
	defer func() {
		for _, lock := range locks {
			_ = lock.Close()
		}
	}()
	for _, entry := range op.Receipt.Mailboxes {
		lock, err := mailboxfence.AcquireMigrationExclusive(ctx, entry.Root)
		if err != nil {
			return op, err
		}
		locks = append(locks, lock)
		if err = handoffCheckRoot(entry); err != nil {
			return op, err
		}
		if err = handoffCheckExistingMarker(op, entry); err != nil {
			return op, err
		}
	}
	// Re-enumerate both archived and live history under every publisher lock on
	// every retry. An interrupted epoch never forgets a late old request/key.
	if err := deps.history(ctx, deps.sourceDatabase, &op); err != nil {
		return op, err
	}
	if err := mailboxhandoff.ValidateReceipt(op.Receipt); err != nil {
		return op, err
	}
	op.Phase = "activating"
	if err := handoffSave(op); err != nil {
		return op, err
	}
	for _, entry := range op.Receipt.Mailboxes {
		marker, e := handoffOwnerMarker(op, entry, "none")
		if e != nil {
			return op, e
		}
		if err = mailboxhandoff.WriteMarker(entry.Root, marker); err != nil {
			return op, err
		}
		if err = handoffArchive(op, entry); err != nil {
			return op, err
		}
	}
	if err := deps.stopped(ctx); err != nil {
		return op, err
	}
	disabled, err := handoffRead(filepath.Join(op.Directory, "prd-disabled.yaml"))
	if err != nil {
		return op, err
	}
	candidate, err := handoffRead(filepath.Join(op.Directory, "qas-handoff.yaml"))
	if err != nil {
		return op, err
	}
	if handoffSHA(disabled) != op.DisabledSourceSHA256 || handoffSHA(candidate) != op.Receipt.TargetConfigSHA256 {
		return op, ErrMailboxHandoff
	}
	if err = handoffReplace(op.SourceConfig, disabled, op.OriginalSourceSHA256, op.DisabledSourceSHA256); err != nil {
		return op, err
	}
	if err = deps.checkpoint("activate-source-config"); err != nil {
		return op, err
	}
	if err = handoffReplace(op.TargetConfig, candidate, op.OriginalTargetSHA256, op.Receipt.TargetConfigSHA256); err != nil {
		return op, err
	}
	if err = deps.checkpoint("activate-target-config"); err != nil {
		return op, err
	}
	raw, err := mailboxhandoff.MarshalReceipt(op.Receipt)
	if err != nil {
		return op, err
	}
	if err = handoffEnsurePrivate(filepath.Dir(op.ReceiptPath)); err != nil {
		return op, err
	}
	if err = handoffWriteSame(op.ReceiptPath, raw); err != nil {
		return op, err
	}
	if err = deps.checkpoint("activate-receipt"); err != nil {
		return op, err
	}
	for index, entry := range op.Receipt.Mailboxes {
		if err = mailboxhandoff.WriteMarker(entry.Root, mailboxhandoff.Marker{OperationID: op.OperationID, OwnerLane: "qas", ReceiptSHA256: handoffSHA(raw), Device: entry.Device, Inode: entry.Inode}); err != nil {
			return op, err
		}
		if index == 0 {
			if err = deps.checkpoint("activate-first-owner-marker"); err != nil {
				return op, err
			}
		}
	}
	op.Phase = "active"
	if err = handoffSave(op); err != nil {
		return op, err
	}
	if err = deps.checkpoint("activate-active-journal"); err != nil {
		return op, err
	}
	return op, nil
}

func handoffOwnerMarker(op MailboxHandoffOperation, entry mailboxhandoff.Mailbox, lane string) (mailboxhandoff.Marker, error) {
	raw, err := mailboxhandoff.MarshalReceipt(op.Receipt)
	if err != nil {
		return mailboxhandoff.Marker{}, err
	}
	return mailboxhandoff.Marker{OperationID: op.OperationID, OwnerLane: lane, ReceiptSHA256: handoffSHA(raw), Device: entry.Device, Inode: entry.Inode}, nil
}

// A stopped runtime is not permission to overwrite another operation's root
// marker. Same-operation markers can be resumed after fresh host/inventory
// checks, including a history-union refresh that changes the receipt digest.
func handoffCheckExistingMarker(op MailboxHandoffOperation, entry mailboxhandoff.Mailbox) error {
	raw, err := handoffReadBounded(filepath.Join(entry.Root, mailboxhandoff.MarkerFileName), 4096)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var marker mailboxhandoff.Marker
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&marker) != nil || marker.OperationID != op.OperationID || marker.Device != entry.Device || marker.Inode != entry.Inode ||
		(marker.OwnerLane != "none" && marker.OwnerLane != "qas") || !prdDigestPattern.MatchString(marker.ReceiptSHA256) {
		return ErrMailboxHandoff
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return ErrMailboxHandoff
	}
	return nil
}

func handoffExpectedConfigs(op MailboxHandoffOperation, active bool) error {
	s, t := op.OriginalSourceSHA256, op.OriginalTargetSHA256
	if active {
		s, t = op.DisabledSourceSHA256, op.Receipt.TargetConfigSHA256
	}
	for path, want := range map[string]string{op.SourceConfig: s, op.TargetConfig: t} {
		raw, e := handoffRead(path)
		if e != nil || handoffSHA(raw) != want {
			return ErrMailboxHandoff
		}
	}
	return nil
}

// RestoreMailboxHandoff returns file ownership to the original PRD installation.
// It never restores a database snapshot or discards QAS artifacts. New QAS
// exchanges must already be terminal and acknowledged; their files are archived
// before the exact PRD directories and configurations are put back.
func RestoreMailboxHandoff(ctx context.Context, op MailboxHandoffOperation) (MailboxHandoffOperation, error) {
	if err := validateMailboxHandoffOperation(op); err != nil {
		return op, err
	}
	unlock, err := handoffLocks(false)
	if err != nil {
		return op, err
	}
	defer unlock()
	return restoreMailboxHandoffLocked(ctx, op, newMailboxHandoffTransactionDeps())
}

func restoreMailboxHandoffLocked(ctx context.Context, op MailboxHandoffOperation, deps mailboxHandoffTransactionDeps) (MailboxHandoffOperation, error) {
	if !deps.valid() {
		return op, ErrMailboxHandoff
	}
	op, err := deps.reload(filepath.Join(op.Directory, "operation.json"))
	if err != nil {
		return op, err
	}
	if op.Phase == "returned" {
		return op, handoffExpectedConfigs(op, false)
	}
	if op.Phase != "active" && op.Phase != "returning" {
		return op, ErrMailboxHandoff
	}
	if err := deps.stopped(ctx); err != nil {
		return op, err
	}
	if err := handoffResumeConfigs(op); err != nil {
		return op, err
	}
	if op.Phase == "active" {
		if err := handoffExpectedConfigs(op, true); err != nil {
			return op, err
		}
		if err := deps.idle(ctx, deps.targetDatabase, true); err != nil {
			return op, err
		}
		db, err := store.OpenExistingRestartPreflightReadOnly(ctx, deps.targetDatabase)
		if err != nil {
			return op, err
		}
		for _, entry := range op.Receipt.Mailboxes {
			var pending int
			err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mailbox_exchanges WHERE mailbox_id=? AND acknowledged_at IS NULL`, entry.TargetID).Scan(&pending)
			if err != nil || pending != 0 {
				db.Close()
				return op, ErrMailboxHandoff
			}
			for _, name := range handoffDirectories {
				d, i, e := handoffIdentity(filepath.Join(entry.Root, name), true)
				if e != nil {
					db.Close()
					return op, e
				}
				op.ReturnDirectories = append(op.ReturnDirectories, handoffDirectory{entry.Root, name, d, i})
			}
		}
		if err = db.Close(); err != nil {
			return op, err
		}
		op.Phase = "returning"
		if err = handoffSave(op); err != nil {
			return op, err
		}
		if err = deps.checkpoint("restore-returning-journal"); err != nil {
			return op, err
		}
	}
	var locks []*mailboxfence.Lock
	defer func() {
		for _, lock := range locks {
			_ = lock.Close()
		}
	}()
	for _, entry := range op.Receipt.Mailboxes {
		lock, err := mailboxfence.AcquireMigrationExclusive(ctx, entry.Root)
		if err != nil {
			return op, err
		}
		locks = append(locks, lock)
		if err = handoffCheckRoot(entry); err != nil {
			return op, err
		}
		if err = handoffCheckExistingMarker(op, entry); err != nil {
			return op, err
		}
	}
	for _, entry := range op.Receipt.Mailboxes {
		marker, e := handoffOwnerMarker(op, entry, "none")
		if e != nil {
			return op, e
		}
		if err = mailboxhandoff.WriteMarker(entry.Root, marker); err != nil {
			return op, err
		}
		qasArchive := filepath.Join(entry.Root, ".runner-mailbox-handoffs", op.OperationID, "qas")
		if err = handoffEnsurePrivate(qasArchive); err != nil {
			return op, err
		}
		for _, original := range op.Directories {
			if original.Root != entry.Root {
				continue
			}
			var current handoffDirectory
			for _, d := range op.ReturnDirectories {
				if d.Root == entry.Root && d.Name == original.Name {
					current = d
					break
				}
			}
			if current.Name == "" {
				return op, ErrMailboxHandoff
			}
			if err = handoffRestoreDirectory(entry.Root, filepath.Join(entry.Root, entry.ArchiveRelative), qasArchive, original, current); err != nil {
				return op, err
			}
			if err = deps.checkpoint("restore-directory-" + original.Name); err != nil {
				return op, err
			}
		}
	}
	for index, item := range []struct{ path, name, expected, want string }{{op.SourceConfig, "prd-original.yaml", op.DisabledSourceSHA256, op.OriginalSourceSHA256}, {op.TargetConfig, "qas-original.yaml", op.Receipt.TargetConfigSHA256, op.OriginalTargetSHA256}} {
		raw, err := handoffRead(filepath.Join(op.Directory, item.name))
		if err != nil || handoffSHA(raw) != item.want {
			return op, ErrMailboxHandoff
		}
		if err = handoffReplace(item.path, raw, item.expected, item.want); err != nil {
			return op, err
		}
		checkpoint := "restore-source-config"
		if index == 1 {
			checkpoint = "restore-target-config"
		}
		if err = deps.checkpoint(checkpoint); err != nil {
			return op, err
		}
	}
	for index, entry := range op.Receipt.Mailboxes {
		marker, e := handoffOwnerMarker(op, entry, "none")
		if e != nil {
			return op, e
		}
		if err := mailboxhandoff.RemoveMarker(entry.Root, marker); err != nil {
			return op, err
		}
		if index == 0 {
			if err = deps.checkpoint("restore-first-marker-removed"); err != nil {
				return op, err
			}
		}
	}
	op.Phase = "returned"
	if err := handoffSave(op); err != nil {
		return op, err
	}
	if err = deps.checkpoint("restore-returned-journal"); err != nil {
		return op, err
	}
	return op, nil
}

func handoffRestoreDirectory(root, prdArchive, qasArchive string, original, current handoffDirectory) error {
	from := filepath.Join(prdArchive, original.Name)
	to := filepath.Join(root, original.Name)
	dev, ino, err := handoffIdentity(from, true)
	if errors.Is(err, os.ErrNotExist) {
		d, i, e := handoffIdentity(to, true)
		qd, qi, qe := handoffIdentity(filepath.Join(qasArchive, current.Name), true)
		if e != nil || qe != nil || d != original.Device || i != original.Inode || qd != current.Device || qi != current.Inode {
			return ErrMailboxHandoff
		}
		return nil
	}
	if err != nil || dev != original.Device || ino != original.Inode {
		return ErrMailboxHandoff
	}
	if err = handoffMoveDirectory(root, qasArchive, current); err != nil {
		return err
	}
	files, err := os.ReadDir(to)
	if err != nil || len(files) != 0 {
		return ErrMailboxHandoff
	}
	if err = os.Remove(to); err != nil {
		return err
	}
	if err = os.Rename(from, to); err != nil {
		return err
	}
	return errors.Join(handoffSync(root), handoffSync(prdArchive))
}

// Client ingress files are owner-written 0644; config/journal files use the
// stricter private reader above. Neither reader follows symlinks.
func handoffReadArtifact(path string, max int64) ([]byte, error) {
	return handoffReadFile(path, max, false)
}

func handoffStopped(ctx context.Context) error {
	prd, _ := BuildPRDUpgradePlan("mac-control-plane")
	qas, _ := BuildCatalogPlan("mac-control-plane", config.ReleaseLaneQAS)
	p, e := NewPRDServiceController()
	if e != nil {
		return e
	}
	q, e := NewSimpleServiceController()
	if e != nil {
		return e
	}
	a, e := p.Status(ctx, prd)
	if e != nil || a {
		return ErrMailboxHandoff
	}
	a, e = q.Status(ctx, qas)
	if e != nil || a {
		return ErrMailboxHandoff
	}
	return nil
}

func handoffIdle(ctx context.Context, path string, strict bool) error {
	db, e := store.OpenExistingRestartPreflightReadOnly(ctx, path)
	if e != nil {
		return e
	}
	defer db.Close()
	i, e := readPRDWorkInventory(ctx, db)
	if e != nil {
		return e
	}
	if !i.InstallableForGuardedRecovery() || i.PendingRecoveryFinalizations != 0 || (strict && !i.Empty()) {
		return ErrMailboxHandoff
	}
	var accepted int
	if e = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mailbox_exchanges WHERE request_state='accepted'`).Scan(&accepted); e != nil {
		return e
	}
	if accepted != 0 {
		return ErrMailboxHandoff
	}
	return nil
}

func handoffNativeRegistry(ctx context.Context, path string, c config.Config) error {
	db, e := store.OpenExistingRestartPreflightReadOnly(ctx, path)
	if e != nil {
		return e
	}
	defer db.Close()
	rows, e := db.QueryContext(ctx, `SELECT mailbox_id,mailbox_root FROM mailbox_configuration_registry`)
	if e != nil {
		return e
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var id, root string
		if e = rows.Scan(&id, &root); e != nil {
			return e
		}
		d, ok := c.Mailbox(id)
		if !ok || d.Root != root {
			return ErrMailboxHandoff
		}
		seen[id] = true
	}
	if e = rows.Err(); e != nil {
		return e
	}
	for _, id := range c.MailboxNames() {
		if !seen[id] {
			return ErrMailboxHandoff
		}
	}
	return nil
}

func handoffCollectHistory(ctx context.Context, path string, op *MailboxHandoffOperation) error {
	db, e := store.OpenExistingRestartPreflightReadOnly(ctx, path)
	if e != nil {
		return e
	}
	defer db.Close()
	for index := range op.Receipt.Mailboxes {
		entry := &op.Receipt.Mailboxes[index]
		ids := map[string]bool{}
		keys := map[string]bool{}
		for _, id := range entry.BlockedRequestIDs {
			ids[id] = true
		}
		for _, key := range entry.BlockedIdempotencySHA256 {
			keys[key] = true
		}
		rows, e := db.QueryContext(ctx, `SELECT client_request_id,client_idempotency_key FROM mailbox_exchanges WHERE mailbox_id=?`, entry.SourceID)
		if e != nil {
			return e
		}
		for rows.Next() {
			var id, key string
			if e = rows.Scan(&id, &key); e != nil {
				rows.Close()
				return e
			}
			ids[id] = true
			keys[handoffSHA([]byte(key))] = true
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		for _, historyRoot := range []string{entry.Root, filepath.Join(entry.Root, entry.ArchiveRelative)} {
			for _, name := range handoffDirectories {
				files, e := os.ReadDir(filepath.Join(historyRoot, name))
				if errors.Is(e, os.ErrNotExist) {
					continue
				}
				if e != nil {
					return e
				}
				for _, file := range files {
					if name != "events" {
						id := strings.TrimSuffix(strings.TrimSuffix(file.Name(), ".json"), ".ready")
						if regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`).MatchString(id) {
							ids[id] = true
						}
					}
					if name == "inbox" && strings.HasSuffix(file.Name(), ".json") {
						raw, e := handoffReadArtifact(filepath.Join(historyRoot, name, file.Name()), 2<<20)
						if e != nil {
							continue
						}
						var request struct {
							IdempotencyKey string `json:"idempotency_key"`
						}
						if json.Unmarshal(raw, &request) == nil && request.IdempotencyKey != "" {
							keys[handoffSHA([]byte(request.IdempotencyKey))] = true
						}
					}
				}
			}
		}
		entry.BlockedRequestIDs = handoffSorted(ids)
		entry.BlockedIdempotencySHA256 = handoffSorted(keys)
	}
	return nil
}

func handoffArchive(op MailboxHandoffOperation, entry mailboxhandoff.Mailbox) error {
	archive := filepath.Join(entry.Root, entry.ArchiveRelative)
	if err := handoffEnsurePrivate(archive); err != nil {
		return err
	}
	for _, d := range op.Directories {
		if d.Root != entry.Root {
			continue
		}
		if err := handoffMoveDirectory(entry.Root, archive, d); err != nil {
			return err
		}
	}
	return nil
}

func handoffMoveDirectory(root, archive string, d handoffDirectory) error {
	from, to := filepath.Join(root, d.Name), filepath.Join(archive, d.Name)
	dev, ino, e := handoffIdentity(to, true)
	if e == nil {
		if dev != d.Device || ino != d.Inode {
			return ErrMailboxHandoff
		}
	} else if errors.Is(e, os.ErrNotExist) {
		dev, ino, e = handoffIdentity(from, true)
		if e != nil || dev != d.Device || ino != d.Inode {
			return ErrMailboxHandoff
		}
		if e = os.Rename(from, to); e != nil {
			return e
		}
		if e = handoffSync(archive); e != nil {
			return e
		}
		if e = handoffSync(root); e != nil {
			return e
		}
	} else {
		return e
	}
	if _, e = os.Lstat(from); errors.Is(e, os.ErrNotExist) {
		if e = os.Mkdir(from, 0700); e != nil {
			return e
		}
		return handoffSync(root)
	}
	if e != nil {
		return e
	}
	dev, ino, e = handoffIdentity(from, true)
	if e != nil || dev == d.Device && ino == d.Inode {
		return ErrMailboxHandoff
	}
	files, e := os.ReadDir(from)
	if e != nil || len(files) != 0 {
		return ErrMailboxHandoff
	}
	return nil
}

func handoffCheckRoot(entry mailboxhandoff.Mailbox) error {
	d, i, e := handoffIdentity(entry.Root, true)
	if e != nil || d != entry.Device || i != entry.Inode {
		return ErrMailboxHandoff
	}
	return nil
}
func handoffSorted(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
func handoffSHA(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func handoffSave(op MailboxHandoffOperation) error {
	raw, e := json.MarshalIndent(op, "", "  ")
	if e != nil {
		return e
	}
	return handoffAtomic(filepath.Join(op.Directory, "operation.json"), append(raw, '\n'))
}
func handoffIdentity(path string, dir bool) (uint64, uint64, error) {
	parent, name, e := handoffOpenParent(path)
	if e != nil {
		return 0, 0, e
	}
	defer unix.Close(parent)
	var s unix.Stat_t
	if e = unix.Fstatat(parent, name, &s, unix.AT_SYMLINK_NOFOLLOW); e != nil {
		return 0, 0, e
	}
	want := uint32(unix.S_IFREG)
	if dir {
		want = unix.S_IFDIR
	}
	if uint32(s.Mode)&unix.S_IFMT != want || s.Uid != uint32(os.Getuid()) || s.Mode&0o077 != 0 || (!dir && s.Nlink != 1) {
		return 0, 0, ErrMailboxHandoff
	}
	return uint64(s.Dev), uint64(s.Ino), nil
}
func handoffRead(path string) ([]byte, error) { return handoffReadBounded(path, 16<<20) }
func handoffReadBounded(path string, max int64) ([]byte, error) {
	return handoffReadFile(path, max, true)
}

func handoffReadFile(path string, max int64, private bool) ([]byte, error) {
	if max < 0 {
		return nil, ErrMailboxHandoff
	}
	parent, name, err := handoffOpenParent(path)
	if err != nil {
		return nil, err
	}
	defer unix.Close(parent)
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return nil, err
	}
	stat, ok := before.Sys().(*syscall.Stat_t)
	if !ok || !before.Mode().IsRegular() || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 || before.Size() > max || before.Mode().Perm()&0o022 != 0 || private && before.Mode().Perm()&0o077 != 0 {
		return nil, ErrMailboxHandoff
	}
	raw, err := io.ReadAll(io.LimitReader(f, max+1))
	after, statErr := f.Stat()
	var current unix.Stat_t
	if err != nil || statErr != nil || int64(len(raw)) > max || int64(len(raw)) != before.Size() || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) ||
		unix.Fstatat(parent, name, &current, unix.AT_SYMLINK_NOFOLLOW) != nil || uint64(current.Dev) != uint64(stat.Dev) || uint64(current.Ino) != uint64(stat.Ino) || current.Mode != stat.Mode || current.Uid != stat.Uid || current.Nlink != 1 {
		return nil, ErrMailboxHandoff
	}
	return raw, nil
}
func handoffLoadConfig(path string) (config.Config, []byte, error) {
	raw, e := handoffRead(path)
	if e != nil {
		return config.Config{}, nil, e
	}
	c, e := config.LoadFile(path)
	if e != nil {
		return c, nil, e
	}
	again, e := handoffRead(path)
	if e != nil || handoffSHA(raw) != handoffSHA(again) || c.SourceSHA256() != handoffSHA(raw) {
		return c, nil, ErrMailboxHandoff
	}
	return c, raw, nil
}
func handoffEnsurePrivate(path string) error {
	fd, err := handoffOpenDirectory(path, true)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Uid != uint32(os.Getuid()) || stat.Mode&0o077 != 0 {
		return ErrMailboxHandoff
	}
	return nil
}
func handoffWriteNew(path string, raw []byte) error {
	parent, name, e := handoffOpenParent(path)
	if e != nil {
		return e
	}
	defer unix.Close(parent)
	fd, e := unix.Openat(parent, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if e != nil {
		return e
	}
	f := os.NewFile(uintptr(fd), name)
	_, e = f.Write(raw)
	e = errors.Join(e, f.Sync(), f.Close())
	if e != nil {
		return e
	}
	return unix.Fsync(parent)
}
func handoffWriteSame(path string, raw []byte) error {
	old, e := handoffRead(path)
	if e == nil {
		if handoffSHA(old) != handoffSHA(raw) {
			return ErrMailboxHandoff
		}
		return nil
	}
	if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	return handoffWriteNew(path, raw)
}
func handoffAtomic(path string, raw []byte) error {
	parent, name, e := handoffOpenParent(path)
	if e != nil {
		return e
	}
	defer unix.Close(parent)
	var nonce [16]byte
	if _, e = rand.Read(nonce[:]); e != nil {
		return e
	}
	temporary := ".handoff-" + hex.EncodeToString(nonce[:])
	fd, e := unix.Openat(parent, temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if e != nil {
		return e
	}
	f := os.NewFile(uintptr(fd), temporary)
	defer unix.Unlinkat(parent, temporary, 0)
	_, e = f.Write(raw)
	e = errors.Join(e, f.Sync(), f.Close())
	if e != nil {
		return e
	}
	if e = unix.Renameat(parent, temporary, parent, name); e != nil {
		return e
	}
	return unix.Fsync(parent)
}
func handoffReplace(path string, raw []byte, expected, already string) error {
	current, e := handoffRead(path)
	if e != nil {
		return e
	}
	digest := handoffSHA(current)
	if digest == already {
		return nil
	}
	if digest != expected {
		return ErrMailboxHandoff
	}
	return handoffAtomic(path, raw)
}
func handoffSync(path string) error {
	fd, e := handoffOpenDirectory(path, false)
	if e != nil {
		return e
	}
	return errors.Join(unix.Fsync(fd), unix.Close(fd))
}

func handoffOpenParent(path string) (int, string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) || strings.ContainsRune(path, 0) {
		return -1, "", ErrMailboxHandoff
	}
	fd, err := handoffOpenDirectory(filepath.Dir(path), false)
	return fd, filepath.Base(path), err
}

// Traverse pinned descriptors rather than following a caller-controlled parent
// symlink. Only macOS's fixed system aliases are normalized, so the native
// temporary directory continues to work without permitting arbitrary aliases.
func handoffOpenDirectory(path string, create bool) (int, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsRune(path, 0) {
		return -1, ErrMailboxHandoff
	}
	if runtime.GOOS == "darwin" {
		for _, alias := range []string{"/var", "/tmp"} {
			if path == alias || strings.HasPrefix(path, alias+"/") {
				path = "/private" + path
				break
			}
		}
	}
	const flags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	fd, err := unix.Open(string(filepath.Separator), flags, 0)
	if err != nil {
		return -1, err
	}
	if path == string(filepath.Separator) {
		return fd, nil
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator)) {
		var stat unix.Stat_t
		if unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || (stat.Uid != 0 && stat.Uid != uint32(os.Getuid())) || (stat.Mode&0o022 != 0 && !(stat.Uid == 0 && stat.Mode&unix.S_ISVTX != 0)) {
			unix.Close(fd)
			return -1, ErrMailboxHandoff
		}
		next, openErr := unix.Openat(fd, part, flags, 0)
		if errors.Is(openErr, unix.ENOENT) && create {
			if e := unix.Mkdirat(fd, part, 0700); e != nil && !errors.Is(e, unix.EEXIST) {
				unix.Close(fd)
				return -1, e
			}
			if e := unix.Fsync(fd); e != nil {
				unix.Close(fd)
				return -1, e
			}
			next, openErr = unix.Openat(fd, part, flags, 0)
		}
		unix.Close(fd)
		if openErr != nil {
			return -1, openErr
		}
		fd = next
	}
	var final unix.Stat_t
	if unix.Fstat(fd, &final) != nil || final.Mode&unix.S_IFMT != unix.S_IFDIR || (final.Uid != 0 && final.Uid != uint32(os.Getuid())) || final.Mode&0o022 != 0 {
		unix.Close(fd)
		return -1, ErrMailboxHandoff
	}
	return fd, nil
}
