package laneinstall

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/mailboxfence"
	"remote-session-runner/src/internal/mailboxhandoff"
)

const f09MailboxRoot = "/Users/tomasz.walczuk/projects/gpu-services/runner/mailbox-"

// WithF09DuplicateRetirementGuard runs an inspection or approved file operation
// only against the registered F09 Mac PRD authority and mailbox. Apply holds
// the catalog, service lifecycle and publisher locks, in that order. Direct
// file writers do not take a publisher lock and must be quiesced by the operator.
func WithF09DuplicateRetirementGuard(ctx context.Context, apply bool, action func(databasePath, mailboxRoot string) (string, error)) (string, error) {
	if ctx == nil || action == nil || !retainedInventoryAccountAndPlatform("mac-control-plane", config.MacAccount) {
		return "", errors.New("F09 retirement requires the Mac control-plane owner account")
	}
	plan, err := BuildPRDUpgradePlan("mac-control-plane")
	if err != nil {
		return "", err
	}
	catalogPlan := Plan{HostID: plan.HostID, Lane: config.ReleaseLanePRD, Namespace: plan.Namespace}
	root, err := DefaultCatalogRootForPlan(catalogPlan)
	if err != nil {
		return "", err
	}
	if apply {
		catalogLock, err := acquireCatalogRootPublicationLock(root)
		if err != nil {
			return "", fmt.Errorf("acquire catalog lock: %w", err)
		}
		defer releaseCatalogRootPublicationLock(catalogLock)
		lifecycleLock, err := acquireScopedTeardownLifecycleLock(plan.Namespace.LifecycleLock)
		if err != nil {
			return "", fmt.Errorf("acquire Mac PRD lifecycle lock: %w", err)
		}
		defer releasePRDUpgradeLock(lifecycleLock)
	}

	selector, err := InspectCatalogSelectorForPlan(catalogPlan, root)
	if err != nil {
		return "", fmt.Errorf("verify selected Mac PRD bundle: %w", err)
	}
	if selector.Kind != "catalog" || selector.Release == nil ||
		selector.Release.Identity.ReleaseID != "rr-20261119-bug034035-7b11dbc" ||
		selector.Release.SourceRevision != "7b11dbc08ad09eb5783c8ab3e5b33917ad2901d6" ||
		selector.Release.Identity.BundleSHA256 != "fdf83edc3b1c9ce8276116199b144505c34bb0a5228e87fa5781f8542df8fe57" {
		return "", errors.New("selected Mac PRD release differs from the inspected incident release")
	}
	cfg, err := config.LoadFile(plan.Namespace.ConfigFile)
	if err != nil {
		return "", fmt.Errorf("load Mac PRD configuration: %w", err)
	}
	settings, ok := cfg.MacSettings()
	if !ok || cfg.Kind() != config.HostKindMac || cfg.ReleaseLane() != config.ReleaseLanePRD ||
		settings.ServiceRoot != plan.Namespace.ServiceRoot || settings.Database != plan.Namespace.Database ||
		settings.Account != config.MacAccount || settings.APISocket != plan.Namespace.APISocket ||
		settings.LocalDSocket != plan.Namespace.LocalDSocket || !settings.MailboxIngressEnabled || settings.MailboxHandoffReceipt != "" {
		return "", errors.New("installed configuration is not the fixed Mac PRD lane")
	}
	box, ok := cfg.Mailbox("gpu-services")
	if !ok || box.Root != f09MailboxRoot || box.ID != "gpu-services" {
		return "", errors.New("installed configuration does not bind the gpu-services mailbox to its original root")
	}
	if _, err := retainedInventoryDatabaseIdentity(settings.Database); err != nil {
		return "", fmt.Errorf("verify fixed Mac PRD database: %w", err)
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		path := settings.Database + suffix
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if _, err := retainedInventoryDatabaseIdentity(path); err != nil {
			return "", fmt.Errorf("verify Mac authority sidecar %s: %w", suffix, err)
		}
	}
	if apply {
		if held, _, err := CatalogCohortAdmissionHeld(catalogPlan); err != nil || held {
			return "", errors.New("cannot retire during a catalog admission hold")
		}
		lock, err := mailboxfence.AcquireMigrationExclusive(ctx, f09MailboxRoot)
		if err != nil {
			return "", fmt.Errorf("acquire mailbox publisher lock: %w", err)
		}
		defer lock.Close()
		if err := lock.VerifyMigrationFenceMarkerAbsent(); err != nil {
			return "", fmt.Errorf("verify mailbox is not migration-fenced: %w", err)
		}
		services, err := NewSimpleServiceController()
		if err != nil {
			return "", fmt.Errorf("initialize Mac service status reader: %w", err)
		}
		loaded := func(target string) (bool, error) {
			return services.macAgentStatus(ctx, filepath.Base(target))
		}
		if err := requireScopedTeardownServicesStoppedWith(plan, loaded, os.Lstat); err != nil {
			return "", fmt.Errorf("both Mac PRD LaunchAgents and sockets must be absent: %w", err)
		}
	}
	marked, err := mailboxhandoff.MarkerExists(box.Root)
	if err != nil || marked {
		return "", errors.New("cannot verify original PRD ownership of the mailbox")
	}
	return action(settings.Database, box.Root)
}
