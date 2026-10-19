package laneinstall

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func TestPinnedSimpleCheckoutAcceptsFrozenSourceWithEvidenceOnlyDescendant(t *testing.T) {
	root, sourceRevision := simplePinnedGitFixture(t, "README.md", "release source\n")
	writeAndCommit(t, root, "040-implementation-evidence/CAT-04.md", "evidence-only closeout\n")
	updateSimpleFixtureOrigin(t, root)
	gitFixture(t, root, "checkout", "--detach", sourceRevision)

	revision, err := requirePinnedSimpleCheckout(context.Background(), root, sourceRevision)
	if err != nil || revision != sourceRevision {
		t.Fatalf("pinned evidence-only source revision=%q err=%v", revision, err)
	}
	if _, err := requireSynchronizedSimpleCheckout(context.Background(), root); !errors.Is(err, ErrInvalidSimpleBundle) {
		t.Fatalf("ordinary synchronized checkout accepted detached frozen source: %v", err)
	}
}

func TestPinnedSimpleCheckoutRejectsSourceChangesDirtyStateAndWrongPin(t *testing.T) {
	t.Run("non-Markdown descendant", func(t *testing.T) {
		root, sourceRevision := simplePinnedGitFixture(t, "README.md", "release source\n")
		writeAndCommit(t, root, "src/changed.go", "package changed\n")
		updateSimpleFixtureOrigin(t, root)
		gitFixture(t, root, "checkout", "--detach", sourceRevision)
		if _, err := requirePinnedSimpleCheckout(context.Background(), root, sourceRevision); !errors.Is(err, ErrInvalidSimpleBundle) {
			t.Fatalf("pinned source accepted a non-Markdown descendant: %v", err)
		}
	})

	t.Run("dirty checkout", func(t *testing.T) {
		root, sourceRevision := simplePinnedGitFixture(t, "README.md", "release source\n")
		if err := os.WriteFile(filepath.Join(root, "untracked.txt"), []byte("untrusted"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := requirePinnedSimpleCheckout(context.Background(), root, sourceRevision); !errors.Is(err, ErrInvalidSimpleBundle) {
			t.Fatalf("pinned source accepted untracked content: %v", err)
		}
	})

	t.Run("wrong expected revision", func(t *testing.T) {
		root, sourceRevision := simplePinnedGitFixture(t, "README.md", "release source\n")
		wrongRevision := "1123456789abcdef0123456789abcdef01234567"
		if wrongRevision == sourceRevision {
			t.Fatal("fixture revisions unexpectedly match")
		}
		if _, err := requirePinnedSimpleCheckout(context.Background(), root, wrongRevision); !errors.Is(err, ErrInvalidSimpleBundle) {
			t.Fatalf("pinned source accepted wrong expected revision: %v", err)
		}
	})
}

func simplePinnedGitFixture(t *testing.T, file, contents string) (string, string) {
	t.Helper()
	root := filepath.Join(simpleTestTempDir(t), "repo")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	gitFixture(t, root, "init", "-b", "dev")
	gitFixture(t, root, "config", "user.name", "Runner Lane Test")
	gitFixture(t, root, "config", "user.email", "runner-lane-test@example.invalid")
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, file)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, file), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	gitFixture(t, root, "add", "--", file)
	gitFixture(t, root, "commit", "-m", "source")
	sourceRevision := strings.TrimSpace(gitFixture(t, root, "rev-parse", "HEAD"))
	updateSimpleFixtureOrigin(t, root)
	return root, sourceRevision
}

func writeAndCommit(t *testing.T, root, file, contents string) {
	t.Helper()
	path := filepath.Join(root, file)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	gitFixture(t, root, "add", "--", file)
	gitFixture(t, root, "commit", "-m", "descendant")
}

func updateSimpleFixtureOrigin(t *testing.T, root string) {
	t.Helper()
	gitFixture(t, root, "update-ref", "refs/remotes/origin/dev", "HEAD")
}

func gitFixture(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}

func TestSimpleBuildEnvironmentEnablesMacProcessIdentityCGO(t *testing.T) {
	for _, test := range []struct {
		name     string
		platform simplePlatform
		wantCGO  string
	}{
		{name: "Mac local runtime", platform: simplePlatform{goos: "darwin", goarch: "arm64"}, wantCGO: "1"},
		{name: "Linux amd64 worker", platform: simplePlatform{goos: "linux", goarch: "amd64"}, wantCGO: "0"},
		{name: "Linux arm64 worker", platform: simplePlatform{goos: "linux", goarch: "arm64"}, wantCGO: "0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			environment := simpleBuildEnvironment([]string{"CGO_ENABLED=0", "GOOS=windows"}, test.platform)
			values := make(map[string]string, len(environment))
			for _, item := range environment {
				name, value, ok := strings.Cut(item, "=")
				if ok {
					values[name] = value
				}
			}
			if values["CGO_ENABLED"] != test.wantCGO || values["GOOS"] != test.platform.goos || values["GOARCH"] != test.platform.goarch || values["GOTOOLCHAIN"] != "local" {
				t.Fatalf("simple build environment = %+v, want %s/%s cgo=%s and local toolchain", values, test.platform.goos, test.platform.goarch, test.wantCGO)
			}
		})
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
