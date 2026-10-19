package laneinstall

import (
	"context"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"remote-session-runner/src/internal/config"
)

func TestPRDLegacyBundleDigestBindsHostAndEveryInstalledFile(t *testing.T) {
	preflight := PRDPreflight{HostID: "linux-host", BinarySHA256: map[string]string{
		"runnerd": strings.Repeat("a", 64), "runner-log-capture": strings.Repeat("b", 64),
	}}
	first, err := PRDLegacyBundleDigest(preflight)
	if err != nil {
		t.Fatal(err)
	}
	preflight.BinarySHA256["runnerd"] = strings.Repeat("c", 64)
	second, err := PRDLegacyBundleDigest(preflight)
	if err != nil || first == second {
		t.Fatalf("legacy identity did not change with binary bytes: first=%s second=%s err=%v", first, second, err)
	}
	preflight.HostID = "sandbox-host"
	third, err := PRDLegacyBundleDigest(preflight)
	if err != nil || second == third {
		t.Fatalf("legacy identity did not change with host identity: second=%s third=%s err=%v", second, third, err)
	}
	preflight.BinarySHA256["../runnerd"] = strings.Repeat("d", 64)
	if _, err := PRDLegacyBundleDigest(preflight); err == nil {
		t.Fatal("legacy identity accepted a path-bearing component name")
	}
}

func TestPRDSelectorMutationRequiresGuardedMaintenanceAndSupportsUnselectedState(t *testing.T) {
	fixture := newCatalogSelectorFixture(t)
	fixture.layout.lane = config.ReleaseLanePRD
	if err := validateCatalogMutationLayout(fixture.layout); err == nil {
		t.Fatal("generic Dev/QAS selector mutation accepted PRD")
	}
	if err := validateCatalogTransitionLayout(fixture.layout); err != nil {
		t.Fatalf("guarded PRD transition layout rejected: %v", err)
	}
	if err := os.Remove(filepath.Join(fixture.layout.serviceRoot, "current")); err != nil {
		t.Fatal(err)
	}
	status, err := inspectCatalogSelector(fixture.layout)
	if err != nil || status.Kind != "unselected" || status.Target != "" {
		t.Fatalf("initial PRD selector status=%+v err=%v", status, err)
	}
	target := filepath.Join(fixture.catalogRoot, fixture.firstRelease, fixture.layout.hostID)
	if err := switchCatalogSelector(fixture.layout, "", target); err != nil {
		t.Fatalf("select initial catalog release: %v", err)
	}
	if err := switchCatalogSelector(fixture.layout, target, ""); err != nil {
		t.Fatalf("return selector to preserved legacy layout: %v", err)
	}
	status, err = inspectCatalogSelector(fixture.layout)
	if err != nil || status.Kind != "unselected" || status.Target != "" {
		t.Fatalf("restored legacy selector status=%+v err=%v", status, err)
	}
}

func TestPRDCatalogArtifactReplacementIsExpectedHashBound(t *testing.T) {
	root := catalogPRDArtifactFixtureDir(t)
	path := filepath.Join(root, "entrypoint.sh")
	old := []byte("#!/bin/sh\nexec /old/bin/runnerd\n")
	if err := os.WriteFile(path, old, 0o700); err != nil {
		t.Fatal(err)
	}
	artifact := Artifact{Kind: ArtifactEntrypoint, InstallPath: path, Mode: 0o700, Contents: "#!/bin/sh\nexec /new/current/bin/runnerd\n"}
	service := &PRDServiceController{}
	if err := replacePRDCatalogArtifact(context.Background(), service, artifact, sha256HexString(old), 0o700); err != nil {
		t.Fatalf("replace backed-up entrypoint: %v", err)
	}
	if err := verifyPRDCatalogArtifact(artifact); err != nil {
		t.Fatalf("replacement did not match exact catalog artifact: %v", err)
	}
	conflict := []byte("#!/bin/sh\nexec /unexpected/runnerd\n")
	if err := os.WriteFile(path, conflict, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := replacePRDCatalogArtifact(context.Background(), service, artifact, sha256HexString(old), 0o700); err == nil {
		t.Fatal("artifact replacement overwrote a file that differed from both expected identities")
	}
	actual, err := os.ReadFile(path)
	if err != nil || string(actual) != string(conflict) {
		t.Fatalf("conflicting artifact was changed: %q err=%v", actual, err)
	}
}

func TestPRDCatalogReplacementSupportsOnlyCurrentAccountOwnedQueuedWrapper(t *testing.T) {
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	group, err := user.LookupGroupId(strconv.Itoa(os.Getgid()))
	if err != nil {
		t.Fatal(err)
	}
	root := catalogPRDArtifactFixtureDir(t)
	path := filepath.Join(root, "runner-ssh-bridge-forced.sh")
	old := []byte("#!/bin/sh\nexec /old/bin/runner-ssh-bridge\n")
	if err := os.WriteFile(path, old, 0o700); err != nil {
		t.Fatal(err)
	}
	artifact := Artifact{Kind: ArtifactSSHWrapper, InstallPath: path, Mode: 0o700, Owner: current.Username, Group: group.Name,
		Contents: "#!/bin/sh\nexec /new/current/bin/runner-ssh-bridge\n"}
	if err := replacePRDCatalogArtifact(context.Background(), &PRDServiceController{}, artifact, sha256HexString(old), 0o700); err != nil {
		t.Fatalf("replace current-account queued wrapper: %v", err)
	}
	if err := verifyPRDCatalogArtifact(artifact); err != nil {
		t.Fatalf("installed queued wrapper does not match expected owner, group, mode, and bytes: %v", err)
	}
	foreign := artifact
	foreign.Owner = "nobody"
	if err := replacePRDCatalogArtifact(context.Background(), &PRDServiceController{}, foreign, sha256HexString([]byte(artifact.Contents)), 0o700); !errors.Is(err, ErrSimpleArtifactConflict) {
		t.Fatalf("replacement did not retain the conflict category for a foreign artifact owner: %v", err)
	}
}

func catalogPRDArtifactFixtureDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}
