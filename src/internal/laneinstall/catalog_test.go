package laneinstall

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"remote-session-runner/src/internal/store"
)

func catalogFixtureBundles(t *testing.T, content string) map[string]SimpleBundle {
	t.Helper()
	bundles := make(map[string]SimpleBundle, len(canonicalCatalogHosts))
	for _, hostID := range canonicalCatalogHosts {
		bundle, err := NewSimpleBundle(hostID, "0123456789abcdef0123456789abcdef01234567", simpleFixtureComponents(hostID, content))
		if err != nil {
			t.Fatalf("create %s catalog fixture: %v", hostID, err)
		}
		bundles[hostID] = bundle
	}
	return bundles
}

func TestCatalogManifestBindsExactlyFourNativeBundles(t *testing.T) {
	bundles := catalogFixtureBundles(t, "release-a")
	manifest, err := NewCatalogReleaseManifest("v1.4.0", bundles)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCatalogReleaseManifest(manifest); err != nil {
		t.Fatalf("validate sealed manifest: %v", err)
	}
	if len(manifest.Hosts) != 4 || manifest.SourceRevision != "0123456789abcdef0123456789abcdef01234567" ||
		manifest.Database.ReadSchemaMax != store.CurrentSchemaVersion || manifest.Database.WriteSchemaMin != store.CurrentSchemaVersion ||
		manifest.Database.WriteSchemaMax != store.CurrentSchemaVersion {
		t.Fatalf("manifest omits cohort identity or schema contract: %+v", manifest)
	}
	for index, hostID := range canonicalCatalogHosts {
		entry := manifest.Hosts[index]
		if entry.HostID != hostID || entry.BundleSHA256 != bundles[hostID].Manifest.BundleSHA256 ||
			entry.BundleManifestSHA256 != simpleBundleManifestSHA256(bundles[hostID].Manifest) {
			t.Fatalf("manifest host[%d] does not bind its native bundle: %+v", index, entry)
		}
	}

	missing := cloneCatalogBundles(bundles)
	delete(missing, "factory-sandbox")
	if _, err := NewCatalogReleaseManifest("v1.4.1", missing); !errors.Is(err, ErrInvalidCatalogRelease) {
		t.Fatalf("missing host was accepted: %v", err)
	}
	wrongSource := cloneCatalogBundles(bundles)
	wrongSource["factory-sandbox"], err = NewSimpleBundle("factory-sandbox", "1123456789abcdef0123456789abcdef01234567", simpleFixtureComponents("factory-sandbox", "release-a"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewCatalogReleaseManifest("v1.4.1", wrongSource); !errors.Is(err, ErrInvalidCatalogRelease) {
		t.Fatalf("different source revision was accepted: %v", err)
	}

	badPlatform := manifest
	badPlatform.Hosts = append([]CatalogHostArtifact(nil), manifest.Hosts...)
	badPlatform.Hosts[0].GOARCH = "arm64"
	if !errors.Is(ValidateCatalogReleaseManifest(badPlatform), ErrInvalidCatalogRelease) {
		t.Fatal("wrong host platform was accepted")
	}
	badDigest := manifest
	badDigest.Hosts = append([]CatalogHostArtifact(nil), manifest.Hosts...)
	badDigest.Hosts[1].BundleManifestSHA256 = "tampered"
	if !errors.Is(ValidateCatalogReleaseManifest(badDigest), ErrInvalidCatalogRelease) {
		t.Fatal("malformed component-manifest digest was accepted")
	}
	if _, err := PublishCatalogRelease(filepath.Join(catalogTestTempDir(t), "catalog"), "linux-host", manifest, bundles["sandbox-host"]); !errors.Is(err, ErrInvalidCatalogRelease) {
		t.Fatalf("another host's native bundle was accepted for publication: %v", err)
	}
}

func TestCatalogReleaseIDRejectsAliasesAndPathTraversal(t *testing.T) {
	for _, releaseID := range []string{"", "dev", "qas", "prd", "latest", "current", "../release", "a/b", `a\\b`, "V1.2.0", strings.Repeat("a", 65)} {
		if validCatalogReleaseID(releaseID) {
			t.Errorf("unsafe or reserved release id %q was accepted", releaseID)
		}
	}
	for _, releaseID := range []string{"v1.2.0", "2026.10.19-1", "r1_4"} {
		if !validCatalogReleaseID(releaseID) {
			t.Errorf("valid immutable release id %q was rejected", releaseID)
		}
	}
}

func TestCatalogPublicationIsIdempotentAndInspectionVerifiesExactIdentity(t *testing.T) {
	bundles := catalogFixtureBundles(t, "release-b")
	manifest, err := NewCatalogReleaseManifest("v1.4.2", bundles)
	if err != nil {
		t.Fatal(err)
	}
	catalogRoot := filepath.Join(catalogTestTempDir(t), "runner-releases")
	first, err := PublishCatalogRelease(catalogRoot, "linux-host", manifest, bundles["linux-host"])
	if err != nil {
		t.Fatalf("first publication: %v", err)
	}
	second, err := PublishCatalogRelease(catalogRoot, "linux-host", manifest, bundles["linux-host"])
	if err != nil || second != first {
		t.Fatalf("exact repeat identity=%+v err=%v; first=%+v", second, err, first)
	}
	status, err := InspectCatalogRelease(catalogRoot, "linux-host", manifest.ReleaseID)
	if err != nil {
		t.Fatalf("inspect published release: %v", err)
	}
	if status.Identity != first || status.SourceRevision != manifest.SourceRevision || status.GOOS != "linux" || status.GOARCH != "amd64" || len(status.Components) != len(simpleHostComponents["linux-host"]) {
		t.Fatalf("inspection identity/status mismatch: %+v", status)
	}
	for path, wantMode := range map[string]os.FileMode{
		filepath.Join(catalogRoot, manifest.ReleaseID):                                 0o500,
		filepath.Join(catalogRoot, manifest.ReleaseID, "release.json"):                 0o400,
		filepath.Join(catalogRoot, manifest.ReleaseID, "linux-host"):                   0o500,
		filepath.Join(catalogRoot, manifest.ReleaseID, "linux-host", "manifest.json"):  0o400,
		filepath.Join(catalogRoot, manifest.ReleaseID, "linux-host", "bin"):            0o500,
		filepath.Join(catalogRoot, manifest.ReleaseID, "linux-host", "bin", "runnerd"): 0o500,
	} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Errorf("stat published catalog path %s: %v", path, err)
			continue
		}
		if info.Mode().Perm() != wantMode {
			t.Errorf("published catalog mode %s = %04o, want %04o", path, info.Mode().Perm(), wantMode)
		}
	}

	manifestPath := filepath.Join(catalogRoot, manifest.ReleaseID, "release.json")
	if err := os.Chmod(filepath.Join(catalogRoot, manifest.ReleaseID), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(manifestPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(manifestPath, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(catalogRoot, manifest.ReleaseID), 0o500); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectCatalogRelease(catalogRoot, "linux-host", manifest.ReleaseID); !errors.Is(err, ErrInvalidCatalogRelease) {
		t.Fatalf("tampered release manifest inspection error=%v", err)
	}
}

func TestCatalogRejectsTamperedBundleUnexpectedFilesAndSymlinkPaths(t *testing.T) {
	bundles := catalogFixtureBundles(t, "release-c")
	manifest, err := NewCatalogReleaseManifest("v1.4.3", bundles)
	if err != nil {
		t.Fatal(err)
	}
	catalogRoot := filepath.Join(catalogTestTempDir(t), "catalog")
	if _, err := PublishCatalogRelease(catalogRoot, "sandbox-host", manifest, bundles["sandbox-host"]); err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(catalogRoot, manifest.ReleaseID, "sandbox-host", "bin", "runnerd")
	if err := os.Chmod(filepath.Join(catalogRoot, manifest.ReleaseID), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(catalogRoot, manifest.ReleaseID, "sandbox-host"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(bundlePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(bundlePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bundlePath, []byte("tampered binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(bundlePath, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(bundlePath), 0o500); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(catalogRoot, manifest.ReleaseID, "sandbox-host"), 0o500); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(catalogRoot, manifest.ReleaseID), 0o500); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectCatalogRelease(catalogRoot, "sandbox-host", manifest.ReleaseID); !errors.Is(err, ErrInvalidCatalogRelease) {
		t.Fatalf("tampered local bundle inspection error=%v", err)
	}

	otherRoot := filepath.Join(catalogTestTempDir(t), "catalog")
	if _, err := PublishCatalogRelease(otherRoot, "linux-host", manifest, bundles["linux-host"]); err != nil {
		t.Fatal(err)
	}
	otherRelease := filepath.Join(otherRoot, manifest.ReleaseID)
	if err := os.Chmod(otherRelease, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(otherRelease, "unexpected"), []byte("extra"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(otherRelease, 0o500); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectCatalogRelease(otherRoot, "linux-host", manifest.ReleaseID); !errors.Is(err, ErrInvalidCatalogRelease) {
		t.Fatalf("unexpected release entry inspection error=%v", err)
	}

	symlinkRoot := filepath.Join(catalogTestTempDir(t), "catalog-link")
	if err := os.Symlink(catalogTestTempDir(t), symlinkRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := PublishCatalogRelease(symlinkRoot, "linux-host", manifest, bundles["linux-host"]); !errors.Is(err, ErrInvalidCatalogRelease) {
		t.Fatalf("symlink catalog root publication error=%v", err)
	}
	if _, err := InspectCatalogRelease(symlinkRoot, "linux-host", manifest.ReleaseID); !errors.Is(err, ErrInvalidCatalogRelease) {
		t.Fatalf("symlink catalog root inspection error=%v", err)
	}
}

func TestCatalogRejectsHardLinkedExecutable(t *testing.T) {
	bundles := catalogFixtureBundles(t, "release-hardlink")
	manifest, err := NewCatalogReleaseManifest("v1.4.3-hardlink", bundles)
	if err != nil {
		t.Fatal(err)
	}
	catalogRoot := filepath.Join(catalogTestTempDir(t), "catalog")
	if _, err := PublishCatalogRelease(catalogRoot, "linux-host", manifest, bundles["linux-host"]); err != nil {
		t.Fatal(err)
	}
	component := filepath.Join(catalogRoot, manifest.ReleaseID, "linux-host", "bin", "runnerd")
	alias := filepath.Join(catalogRoot, "linked-runnerd")
	if err := os.Link(component, alias); err != nil {
		t.Fatalf("create hard-link fixture: %v", err)
	}
	if _, err := InspectCatalogRelease(catalogRoot, "linux-host", manifest.ReleaseID); !errors.Is(err, ErrInvalidCatalogRelease) {
		t.Fatalf("hard-linked executable inspection error=%v", err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogConcurrentConflictingPublicationCannotOverwriteSealedRelease(t *testing.T) {
	firstBundles := catalogFixtureBundles(t, "release-first")
	secondBundles := catalogFixtureBundles(t, "release-second")
	firstManifest, err := NewCatalogReleaseManifest("v1.4.4", firstBundles)
	if err != nil {
		t.Fatal(err)
	}
	secondManifest, err := NewCatalogReleaseManifest("v1.4.4", secondBundles)
	if err != nil {
		t.Fatal(err)
	}
	catalogRoot := filepath.Join(catalogTestTempDir(t), "catalog")
	start := make(chan struct{})
	results := make(chan error, 2)
	var group sync.WaitGroup
	for _, candidate := range []struct {
		manifest CatalogReleaseManifest
		bundle   SimpleBundle
	}{{firstManifest, firstBundles["factory-sandbox"]}, {secondManifest, secondBundles["factory-sandbox"]}} {
		candidate := candidate
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			_, err := PublishCatalogRelease(catalogRoot, "factory-sandbox", candidate.manifest, candidate.bundle)
			results <- err
		}()
	}
	close(start)
	group.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrCatalogReleaseConflict):
			conflicts++
		default:
			t.Fatalf("concurrent publication returned unexpected error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent publications succeeded=%d conflicts=%d; want 1 each", successes, conflicts)
	}
	status, err := InspectCatalogRelease(catalogRoot, "factory-sandbox", firstManifest.ReleaseID)
	if err != nil {
		t.Fatalf("winning release is not inspectable: %v", err)
	}
	if status.Identity.BundleSHA256 != firstManifest.Hosts[0].BundleSHA256 && status.Identity.BundleSHA256 != secondManifest.Hosts[0].BundleSHA256 {
		t.Fatalf("published bundle is neither complete candidate: %+v", status.Identity)
	}
}

func TestCatalogInterruptedStagingIsNotSelectable(t *testing.T) {
	catalogRoot := filepath.Join(catalogTestTempDir(t), "catalog")
	if err := os.MkdirAll(catalogRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	interrupted := filepath.Join(catalogRoot, ".catalog-stage-interrupted")
	if err := os.Mkdir(interrupted, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectCatalogRelease(catalogRoot, "linux-host", "v1.4.5"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("incomplete staging directory became selectable: %v", err)
	}
	bundles := catalogFixtureBundles(t, "release-d")
	manifest, err := NewCatalogReleaseManifest("v1.4.5", bundles)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PublishCatalogRelease(catalogRoot, "linux-host", manifest, bundles["linux-host"]); err != nil {
		t.Fatalf("publish beside incomplete staging directory: %v", err)
	}
}

func cloneCatalogBundles(source map[string]SimpleBundle) map[string]SimpleBundle {
	cloned := make(map[string]SimpleBundle, len(source))
	for hostID, bundle := range source {
		cloned[hostID] = bundle
	}
	return cloned
}

func catalogTestTempDir(t *testing.T) string {
	t.Helper()
	root := simpleTestTempDir(t)
	t.Cleanup(func() {
		_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil || entry.Type()&os.ModeSymlink != 0 {
				return nil
			}
			if entry.IsDir() {
				_ = os.Chmod(path, 0o700)
			} else if entry.Type().IsRegular() {
				_ = os.Chmod(path, 0o600)
			}
			return nil
		})
	})
	return root
}
