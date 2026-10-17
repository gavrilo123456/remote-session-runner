package laneinstall

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/store"
)

func TestSimpleBundlePinsExactPerHostPlatformAndComponents(t *testing.T) {
	for host, names := range simpleHostComponents {
		components := make(map[string][]byte, len(names))
		for _, name := range names {
			components[name] = []byte("fixture executable: " + host + ":" + name)
		}
		bundle, err := NewSimpleBundle(host, "0123456789abcdef0123456789abcdef01234567", components)
		if err != nil {
			t.Fatalf("NewSimpleBundle(%s): %v", host, err)
		}
		if err := ValidateSimpleBundle(bundle); err != nil {
			t.Fatalf("ValidateSimpleBundle(%s): %v", host, err)
		}
		platform := simpleHostPlatforms[host]
		if bundle.Manifest.GOOS != platform.goos || bundle.Manifest.GOARCH != platform.goarch ||
			bundle.Manifest.Toolchain != "go1.27.1" || bundle.Manifest.DatabaseSchemaMax != store.CurrentSchemaVersion ||
			len(bundle.Manifest.Components) != len(names) || len(bundle.Manifest.BundleSHA256) != 64 {
			t.Fatalf("incomplete candidate manifest for %s: %+v", host, bundle.Manifest)
		}
		for _, component := range bundle.Manifest.Components {
			if component.Bytes == 0 || component.SHA256 == "" {
				t.Fatalf("component digest missing for %s/%s: %+v", host, component.Name, component)
			}
		}
	}
}

func TestSimpleBundleRejectsMissingExtraTamperedOrForeignComponents(t *testing.T) {
	components := simpleFixtureComponents("linux-host", "one")
	if _, err := NewSimpleBundle("linux-host", "0123456789abcdef0123456789abcdef01234567", components); err != nil {
		t.Fatal(err)
	}
	for name, bad := range map[string]struct {
		host, revision string
		components     map[string][]byte
	}{
		"missing component": {host: "linux-host", revision: "0123456789abcdef0123456789abcdef01234567", components: removeSimpleComponent(components, "runnerd")},
		"extra component":   {host: "linux-host", revision: "0123456789abcdef0123456789abcdef01234567", components: addSimpleComponent(components, "runner-local", []byte("foreign"))},
		"foreign host set":  {host: "mac-control-plane", revision: "0123456789abcdef0123456789abcdef01234567", components: components},
		"invalid revision":  {host: "linux-host", revision: "dirty", components: components},
	} {
		if _, err := NewSimpleBundle(bad.host, bad.revision, bad.components); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	bundle, err := NewSimpleBundle("linux-host", "0123456789abcdef0123456789abcdef01234567", components)
	if err != nil {
		t.Fatal(err)
	}
	bundle.Components["runnerd"][0] ^= 0xff
	if !errors.Is(ValidateSimpleBundle(bundle), ErrInvalidSimpleBundle) {
		t.Fatal("tampered component bytes were accepted")
	}
}

func TestSimpleBundleStagingIsImmutableAndRejectsSymlinks(t *testing.T) {
	bundle, err := NewSimpleBundle("linux-host", "0123456789abcdef0123456789abcdef01234567", simpleFixtureComponents("linux-host", "one"))
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(simpleTestTempDir(t), "staging", "bundles", bundle.Manifest.BundleSHA256)
	if err := writeSimpleBundleAt(destination, bundle); err != nil {
		t.Fatal(err)
	}
	if err := writeSimpleBundleAt(destination, bundle); err != nil {
		t.Fatalf("identical immutable staging retry failed: %v", err)
	}
	loaded, err := readSimpleBundleAt(destination)
	if err != nil || !bundlesEqual(loaded, bundle) {
		t.Fatalf("read staged bundle=%v err=%v", loaded.Manifest.BundleSHA256, err)
	}
	if err := os.WriteFile(filepath.Join(destination, "bin", "runnerd"), []byte("modified"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeSimpleBundleAt(destination, bundle); !errors.Is(err, ErrSimpleBundleExists) {
		t.Fatalf("modified immutable candidate write error=%v", err)
	}

	parent := filepath.Join(simpleTestTempDir(t), "real")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "linked")
	if err := os.Symlink(filepath.Join(simpleTestTempDir(t), "elsewhere"), link); err != nil {
		t.Fatal(err)
	}
	if err := writeSimpleBundleAt(filepath.Join(link, "bundle"), bundle); !errors.Is(err, ErrInvalidSimpleBundle) {
		t.Fatalf("symlinked staging path error=%v", err)
	}
}

func TestSimpleActivationAndRollbackTouchOnlySelectedLane(t *testing.T) {
	root := simpleTestTempDir(t)
	prdRoot := filepath.Join(root, "prd")
	qasRoot := filepath.Join(root, "qas")
	devRoot := filepath.Join(root, "dev")
	for _, directory := range []string{prdRoot, qasRoot, devRoot} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	prdMarker := filepath.Join(prdRoot, "authority.db")
	qasMarker := filepath.Join(qasRoot, "authority.db")
	if err := os.WriteFile(prdMarker, []byte("prd retained state"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(qasMarker, []byte("qas retained state"), 0o600); err != nil {
		t.Fatal(err)
	}
	prdBefore, _ := os.ReadFile(prdMarker)
	qasBefore, _ := os.ReadFile(qasMarker)

	first, err := NewSimpleBundle("linux-host", "0123456789abcdef0123456789abcdef01234567", simpleFixtureComponents("linux-host", "first"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewSimpleBundle("linux-host", "1123456789abcdef0123456789abcdef01234567", simpleFixtureComponents("linux-host", "second"))
	if err != nil {
		t.Fatal(err)
	}
	firstActivation, err := installSimpleBundleAt(devRoot, "linux-host", config.ReleaseLaneDev, first)
	if err != nil || firstActivation.PreviousBundleSHA256() != "" {
		t.Fatalf("first Dev activation=%+v err=%v", firstActivation, err)
	}
	secondActivation, err := installSimpleBundleAt(devRoot, "linux-host", config.ReleaseLaneDev, second)
	if err != nil || secondActivation.PreviousBundleSHA256() != first.Manifest.BundleSHA256 {
		t.Fatalf("second Dev activation=%+v err=%v", secondActivation, err)
	}
	if current, err := simpleCurrentBundle(devRoot); err != nil || current != second.Manifest.BundleSHA256 {
		t.Fatalf("Dev active bundle=%s err=%v", current, err)
	}
	if err := rollbackSimpleBundleAt(devRoot, "linux-host", config.ReleaseLaneDev, secondActivation, store.CurrentSchemaVersion); err != nil {
		t.Fatalf("compatible binary-only rollback failed: %v", err)
	}
	if current, err := simpleCurrentBundle(devRoot); err != nil || current != first.Manifest.BundleSHA256 {
		t.Fatalf("Dev rollback target=%s err=%v", current, err)
	}
	prdAfter, _ := os.ReadFile(prdMarker)
	qasAfter, _ := os.ReadFile(qasMarker)
	if string(prdAfter) != string(prdBefore) || string(qasAfter) != string(qasBefore) {
		t.Fatal("Dev activation or rollback changed PRD/QAS fixture state")
	}
	if _, err := os.Lstat(filepath.Join(qasRoot, "current")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Dev action created a QAS activation: %v", err)
	}
}

func TestSimpleRollbackRejectsIncompatibleDatabaseAndStaleReceipt(t *testing.T) {
	root := simpleTestTempDir(t)
	first, err := NewSimpleBundle("factory-sandbox", "0123456789abcdef0123456789abcdef01234567", simpleFixtureComponents("factory-sandbox", "first"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewSimpleBundle("factory-sandbox", "1123456789abcdef0123456789abcdef01234567", simpleFixtureComponents("factory-sandbox", "second"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := installSimpleBundleAt(root, "factory-sandbox", config.ReleaseLaneQAS, first); err != nil {
		t.Fatal(err)
	}
	activation, err := installSimpleBundleAt(root, "factory-sandbox", config.ReleaseLaneQAS, second)
	if err != nil {
		t.Fatal(err)
	}
	if err := rollbackSimpleBundleAt(root, "factory-sandbox", config.ReleaseLaneQAS, activation, store.CurrentSchemaVersion+1); !errors.Is(err, ErrSimpleRollbackUnavailable) {
		t.Fatalf("rollback to an executable below the DB schema was allowed: %v", err)
	}
	if current, err := simpleCurrentBundle(root); err != nil || current != second.Manifest.BundleSHA256 {
		t.Fatalf("incompatible rollback changed current release: %s err=%v", current, err)
	}
	if _, err := installSimpleBundleAt(root, "factory-sandbox", config.ReleaseLaneQAS, first); err != nil {
		t.Fatal(err)
	}
	if err := rollbackSimpleBundleAt(root, "factory-sandbox", config.ReleaseLaneQAS, activation, store.CurrentSchemaVersion); !errors.Is(err, ErrSimpleActivationConflict) {
		t.Fatalf("stale activation receipt error=%v", err)
	}
}

func simpleFixtureComponents(hostID, version string) map[string][]byte {
	components := make(map[string][]byte, len(simpleHostComponents[hostID]))
	for _, name := range simpleHostComponents[hostID] {
		components[name] = []byte("fixture executable " + name + " " + version)
	}
	return components
}

func simpleTestTempDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func removeSimpleComponent(components map[string][]byte, name string) map[string][]byte {
	copyComponents := make(map[string][]byte, len(components)-1)
	for key, value := range components {
		if key != name {
			copyComponents[key] = value
		}
	}
	return copyComponents
}

func addSimpleComponent(components map[string][]byte, name string, data []byte) map[string][]byte {
	copyComponents := make(map[string][]byte, len(components)+1)
	for key, value := range components {
		copyComponents[key] = value
	}
	copyComponents[name] = data
	return copyComponents
}
