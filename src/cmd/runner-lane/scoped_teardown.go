package main

import (
	"context"
	"errors"
	"flag"
	"io"

	"remote-session-runner/src/internal/laneinstall"
)

const scopedTeardownUsage = "usage: runner-lane catalog-prd-recover-scoped-teardown --host mac-control-plane --teardown-pair SESSION:COMMAND --release-id RELEASE --release-sha256 SHA256 --bundle-sha256 SHA256 (--plan | --apply --configuration-sha256 SHA256 --backup-dir DIR --backup-manifest-sha256 SHA256 --preserved-inventory-sha256 SHA256 --preserved-ownership-sha256 SHA256 --target-ownership-sha256 SHA256)"

// The fixture backend is package-private and is populated only by subprocess
// tests. No production flag, environment variable or path selects it.
type scopedTeardownCommandFixture interface {
	RunScopedTeardown(context.Context, laneinstall.CatalogPRDScopedTeardownRequest, bool, io.Writer, io.Writer) error
}

var scopedTeardownCommandFixtureForTests scopedTeardownCommandFixture

func runCatalogPRDScopedTeardown(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("runner-lane catalog-prd-recover-scoped-teardown", flag.ContinueOnError)
	flags.SetOutput(stderr)
	host := flags.String("host", "", "mac-control-plane only")
	planOnly := flags.Bool("plan", false, "read-only applicability and preservation fingerprints; never authorizes recovery")
	apply := flags.Bool("apply", false, "perform only the exact separately approved offline teardown recovery")
	releaseID := flags.String("release-id", "", "current selected sealed release ID")
	releaseSHA := flags.String("release-sha256", "", "exact selected release manifest SHA-256")
	bundleSHA := flags.String("bundle-sha256", "", "exact selected Mac bundle SHA-256")
	configurationSHA := flags.String("configuration-sha256", "", "exact reviewed current configuration SHA-256")
	backupDir := flags.String("backup-dir", "", "fresh verified pre-recovery backup directory")
	backupSHA := flags.String("backup-manifest-sha256", "", "exact verified backup manifest SHA-256")
	preservedInventorySHA := flags.String("preserved-inventory-sha256", "", "reviewed preservation fingerprint; retain across retries")
	preservedOwnershipSHA := flags.String("preserved-ownership-sha256", "", "reviewed peer ownership fingerprint; retain across retries")
	targetOwnershipSHA := flags.String("target-ownership-sha256", "", "reviewed immutable target/workspace fingerprint; retain across retries")
	runtimeArchiveDir := flags.String("runtime-archive-dir", "", "original immutable target archive when retrying with a fresh catalog backup")
	runtimeArchiveSHA := flags.String("runtime-archive-manifest-sha256", "", "exact original runtime archive manifest SHA-256; pair with runtime-archive-dir")
	var pair string
	flags.Func("teardown-pair", "exactly one completed-command SESSION:COMMAND pair", func(value string) error {
		if pair != "" || value == "" {
			return errors.New("exactly one nonempty --teardown-pair is required")
		}
		pair = value
		return nil
	})
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || *host != "mac-control-plane" || pair == "" ||
		*releaseID == "" || *releaseSHA == "" || *bundleSHA == "" || *planOnly == *apply {
		return errors.New(scopedTeardownUsage)
	}
	if *apply && (*configurationSHA == "" || *backupDir == "" || *backupSHA == "" ||
		*preservedInventorySHA == "" || *preservedOwnershipSHA == "" || *targetOwnershipSHA == "") {
		return errors.New(scopedTeardownUsage)
	}
	request := laneinstall.CatalogPRDScopedTeardownRequest{
		ReleaseID: *releaseID, ExpectedReleaseSHA256: *releaseSHA, ExpectedBundleSHA256: *bundleSHA,
		ExpectedConfigurationSHA256: *configurationSHA, TeardownPair: pair,
		BackupDirectory: *backupDir, ExpectedBackupManifestSHA: *backupSHA,
		ExpectedPreservedInventorySHA256:     *preservedInventorySHA,
		ExpectedPreservedOwnershipSHA256:     *preservedOwnershipSHA,
		ExpectedTargetOwnershipSHA256:        *targetOwnershipSHA,
		RuntimeArchiveDirectory:              *runtimeArchiveDir,
		ExpectedRuntimeArchiveManifestSHA256: *runtimeArchiveSHA,
	}
	if err := laneinstall.ValidateCatalogPRDScopedTeardownRequest(request, *apply); err != nil {
		return err
	}
	if scopedTeardownCommandFixtureForTests != nil {
		return scopedTeardownCommandFixtureForTests.RunScopedTeardown(ctx, request, *planOnly, stdout, stderr)
	}
	if !currentCatalogHost(*host) {
		return errors.New("scoped teardown recovery must run on the selected Mac authority")
	}
	plan, err := laneinstall.BuildPRDUpgradePlan(*host)
	if err != nil {
		return err
	}
	service, err := laneinstall.NewPRDServiceController()
	if err != nil {
		return err
	}
	var report laneinstall.CatalogPRDScopedTeardownReport
	if *planOnly {
		report, err = laneinstall.InspectCatalogPRDScopedTeardown(ctx, plan, service, request)
	} else {
		report, err = laneinstall.RecoverCatalogPRDScopedTeardown(ctx, plan, service, request)
	}
	if err != nil {
		return err
	}
	return writeJSON(stdout, report)
}
