package laneinstall

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/config"
)

func TestBUG028SharedMacTrustPreservesPeerAndReplacementCertificateChains(t *testing.T) {
	now := time.Now().UTC()
	sandboxCA, sandboxKey := prdReplacementTestCA(t, now)
	factoryCA, factoryKey := prdReplacementTestCA(t, now)
	linuxCA, linuxKey := prdReplacementTestCA(t, now)
	previous := append(append(append([]byte("\n"), sandboxCA...), factoryCA...), []byte("\n  \n")...)
	mac := bug028SharedMacConfiguration(t)
	merged, err := buildPRDReplacementMacServerTrust(mac, previous, linuxCA, now)
	if err != nil {
		t.Fatalf("shared trust replacement: %v", err)
	}
	if !bytes.HasPrefix(merged, previous) {
		t.Fatal("shared update rewrote the original approved trust bytes")
	}
	if got := bytes.Count(merged, []byte("-----BEGIN CERTIFICATE-----")); got != 3 {
		t.Fatalf("CA count=%d, want the two approved peer roots plus replacement root", got)
	}
	for _, leaf := range []struct {
		name, address string
		root, key     []byte
	}{
		{"sandbox-host", "132.226.205.205", sandboxCA, sandboxKey},
		{"factory-sandbox", "145.241.255.66", factoryCA, factoryKey},
		{"linux-host", "79.72.94.59", linuxCA, linuxKey},
	} {
		t.Run(leaf.name, func(t *testing.T) {
			certificate := bug028ServerCertificate(t, now, leaf.root, leaf.key, leaf.address)
			bug028RequireServerTrust(t, merged, certificate, leaf.address, now, true)
			if leaf.name != "linux-host" {
				// This is the incident: overwriting the shared file with only the
				// replacement root rejects the unchanged peer's real certificate.
				bug028RequireServerTrust(t, linuxCA, certificate, leaf.address, now, false)
			}
		})
	}
	repeated, err := buildPRDReplacementMacServerTrust(mac, merged, linuxCA, now)
	if err != nil || !bytes.Equal(repeated, merged) {
		t.Fatalf("already-present replacement root was duplicated or rewritten: err=%v", err)
	}
}

func TestBUG028UnsharedMacTrustRotationDoesNotRetainOldRoot(t *testing.T) {
	now := time.Now().UTC()
	oldCA, oldKey := prdReplacementTestCA(t, now)
	newCA, newKey := prdReplacementTestCA(t, now)
	got, err := buildPRDReplacementMacServerTrust([]byte(prdReplacementBootstrapMacConfig), oldCA, newCA, now)
	if err != nil || !bytes.Equal(got, newCA) {
		t.Fatalf("unshared root rotation changed semantics: err=%v", err)
	}
	bug028RequireServerTrust(t, got, bug028ServerCertificate(t, now, newCA, newKey, "79.72.94.59"), "79.72.94.59", now, true)
	bug028RequireServerTrust(t, got, bug028ServerCertificate(t, now, oldCA, oldKey, "129.151.232.40"), "129.151.232.40", now, false)
}

func TestBUG028SharedMacTrustRejectsUnvalidatedAuthorities(t *testing.T) {
	now := time.Now().UTC()
	oldCA, oldKey := prdReplacementTestCA(t, now)
	newCA, _ := prdReplacementTestCA(t, now)
	expired, _ := prdReplacementTestCA(t, now.Add(-366*24*time.Hour))
	future, _ := prdReplacementTestCA(t, now.Add(2*time.Hour))
	leaf := bug028ServerCertificate(t, now, oldCA, oldKey, "132.226.205.205")
	tests := []struct {
		name      string
		old, next []byte
	}{
		{"missing original", nil, newCA},
		{"malformed original", []byte("not a certificate"), newCA},
		{"leading garbage", append([]byte("unexpected\n"), oldCA...), newCA},
		{"trailing garbage", append(append([]byte(nil), oldCA...), []byte("unexpected\n")...), newCA},
		{"private key in trust", append(append([]byte(nil), oldCA...), oldKey...), newCA},
		{"leaf used as authority", leaf, newCA},
		{"expired authority", expired, newCA},
		{"future authority", future, newCA},
		{"oversized original", bytes.Repeat(oldCA, (1<<20)/len(oldCA)+1), newCA},
		{"too many roots", bytes.Repeat(oldCA, 33), newCA},
		{"missing replacement", oldCA, nil},
		{"invalid replacement", oldCA, leaf},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := append([]byte(nil), test.old...)
			if _, err := buildPRDReplacementMacServerTrust(bug028SharedMacConfiguration(t), test.old, test.next, now); err == nil {
				t.Fatal("unvalidated trust was accepted")
			}
			if !bytes.Equal(before, test.old) {
				t.Fatal("rejected trust planning modified the input bytes")
			}
		})
	}
}

func TestBUG028ImmutableOriginalCABackupIsBoundToExactManifest(t *testing.T) {
	now := time.Now().UTC()
	ca, _ := prdReplacementTestCA(t, now)
	fixture := bug028CABackup(t, ca)
	got, err := readPRDReplacementMacRouteServerCABackup(fixture.manifest, fixture.target, digestBytes(ca))
	if err != nil || !bytes.Equal(got, ca) {
		t.Fatalf("read exact approved original CA: err=%v", err)
	}
	tests := []struct {
		name   string
		mutate func(*bug028BackupFixture)
	}{
		{"wrong target", func(f *bug028BackupFixture) { f.manifest.Files[0].TargetPath += "-other" }},
		{"wrong recorded hash", func(f *bug028BackupFixture) { f.manifest.Files[0].SHA256 = strings.Repeat("a", 64) }},
		{"wrong byte length", func(f *bug028BackupFixture) { f.manifest.Files[0].Bytes++ }},
		{"wrong mode", func(f *bug028BackupFixture) { bug028Must(t, os.Chmod(f.caPath, 0o644)) }},
		{"changed bytes", func(f *bug028BackupFixture) { bug028Must(t, os.WriteFile(f.caPath, []byte("changed"), 0o600)) }},
		{"duplicate target", func(f *bug028BackupFixture) { f.manifest.Files = append(f.manifest.Files, f.manifest.Files[0]) }},
		{"escaping backup path", func(f *bug028BackupFixture) { f.manifest.Files[0].BackupPath = "../outside.pem" }},
		{"missing database snapshot", func(f *bug028BackupFixture) { bug028Must(t, os.Remove(f.manifest.DatabaseBackupPath)) }},
		{"hardlinked CA", func(f *bug028BackupFixture) { bug028Must(t, os.Link(f.caPath, f.caPath+".link")) }},
		{"symlinked CA", func(f *bug028BackupFixture) {
			bug028Must(t, os.Rename(f.caPath, f.caPath+".real"))
			bug028Must(t, os.Symlink(f.caPath+".real", f.caPath))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := bug028CABackup(t, ca)
			test.mutate(&f)
			if _, err := readPRDReplacementMacRouteServerCABackup(f.manifest, f.target, digestBytes(ca)); err == nil {
				t.Fatal("inconsistent immutable original backup was accepted")
			}
		})
	}
	if _, err := readPRDReplacementMacRouteServerCABackup(fixture.manifest, fixture.target, strings.Repeat("b", 64)); err == nil {
		t.Fatal("caller expectation did not bind the original CA digest")
	}
}

func TestBUG028RouteTrustGuardRejectsDestructiveHistoryAndLiveDriftWithoutWrites(t *testing.T) {
	now := time.Now().UTC()
	oldCA, _ := prdReplacementTestCA(t, now)
	newCA, _ := prdReplacementTestCA(t, now)
	unrelatedCA, _ := prdReplacementTestCA(t, now)
	mac := bug028SharedMacConfiguration(t)
	merged, err := buildPRDReplacementMacServerTrust(mac, oldCA, newCA, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		current []byte
		target  []byte
		wantErr error
	}{
		{"legacy Linux-only after-state", newCA, newCA, ErrCatalogOperationInvalid},
		{"owner-repaired two-CA after-state", merged, newCA, ErrCatalogOperationInvalid},
		{"unexpected live CA under valid journal", unrelatedCA, merged, ErrCatalogSelectorConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			backup := bug028CABackup(t, oldCA)
			root := bug028PrivateTempDir(t)
			currentPath := filepath.Join(root, "shared-ca.pem")
			journalPath := filepath.Join(root, "legacy-mac-route.json")
			bug028Must(t, os.WriteFile(currentPath, test.current, 0o600))
			// Readable history must not endorse a destructive recorded target
			// or an unrelated live CA before the installer starts its effects.
			journal := prdReplacementMacRouteJournal{FormatVersion: 1,
				OperationID: "0123456789abcdef0123456789abcdef", RequestSHA256: digestBytes([]byte("original input")), Stage: "complete",
				PreviousConfigurationSHA256: digestBytes(mac), ConfigurationSHA256: digestBytes([]byte("replacement route")),
				PreviousServerCASHA256: digestBytes(oldCA), ServerCASHA256: digestBytes(test.target),
				PreviousKnownHostsSHA256: digestBytes([]byte("old pin")), KnownHostsSHA256: digestBytes([]byte("new pin")),
				WorkInventorySHA256: (PRDWorkInventory{}).SHA256(), DatabaseSchema: 40,
				PreviousBundleSHA256: digestBytes([]byte("previous bundle")),
				BackupDirectory:      filepath.Dir(backup.manifest.DatabaseBackupPath), BackupManifestSHA256: digestBytes([]byte("caller-bound backup manifest")),
				CreatedAtUTC: now.Format(time.RFC3339Nano), UpdatedAtUTC: now.Format(time.RFC3339Nano)}
			bug028Must(t, writePRDReplacementMacRouteJournal(journalPath, journal, false))
			recordedBytes, err := os.ReadFile(journalPath)
			bug028Must(t, err)
			recorded, found, err := readPRDReplacementMacRouteJournal(journalPath)
			if err != nil || !found {
				t.Fatalf("valid historical journal no longer readable: found=%t err=%v", found, err)
			}
			if _, err := preparePRDReplacementMacRouteServerTrust(mac, test.current, newCA, &recorded, &backup.manifest, now); !errors.Is(err, test.wantErr) {
				t.Fatalf("unsafe trust state returned %v, want %v", err, test.wantErr)
			}
			currentAfter, err := os.ReadFile(currentPath)
			bug028Must(t, err)
			journalAfter, err := os.ReadFile(journalPath)
			bug028Must(t, err)
			if !bytes.Equal(currentAfter, test.current) || !bytes.Equal(journalAfter, recordedBytes) {
				t.Fatal("pre-effect refusal changed live trust or historical journal bytes")
			}
		})
	}
}

func TestBUG028RouteTrustGuardRequiresBoundIntentOrImmutableBackup(t *testing.T) {
	now := time.Now().UTC()
	oldCA, _ := prdReplacementTestCA(t, now)
	newCA, _ := prdReplacementTestCA(t, now)
	mac := bug028SharedMacConfiguration(t)
	candidate, err := preparePRDReplacementMacRouteServerTrust(mac, oldCA, newCA, nil, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	intent := prdReplacementMacRouteJournal{Stage: "intent", PreviousServerCASHA256: digestBytes(oldCA), ServerCASHA256: digestBytes(candidate)}
	got, err := preparePRDReplacementMacRouteServerTrust(mac, oldCA, newCA, &intent, nil, now)
	if err != nil || !bytes.Equal(got, candidate) {
		t.Fatalf("unchanged pre-backup intent cannot resume: %v", err)
	}
	if _, err := preparePRDReplacementMacRouteServerTrust(mac, candidate, newCA, &intent, nil, now); err == nil {
		t.Fatal("intent without a backup accepted changed live CA bytes")
	}
	backup := bug028CABackup(t, oldCA)
	journal := intent
	journal.BackupDirectory = filepath.Dir(backup.manifest.DatabaseBackupPath)
	for _, stage := range []string{"backed_up", "known_hosts_updated", "server_trust_updated", "complete"} {
		for _, current := range [][]byte{oldCA, candidate} {
			journal.Stage = stage
			got, err := preparePRDReplacementMacRouteServerTrust(mac, current, newCA, &journal, &backup.manifest, now)
			if err != nil || !bytes.Equal(got, candidate) {
				t.Fatalf("recorded %s retry was not bound to immutable original: %v", stage, err)
			}
		}
	}
	if _, err := preparePRDReplacementMacRouteServerTrust(mac, candidate, newCA, &journal, nil, now); err == nil {
		t.Fatal("recorded backup could be omitted on retry")
	}
	if _, err := preparePRDReplacementMacRouteServerTrust(mac, oldCA, newCA, nil, &backup.manifest, now); err == nil {
		t.Fatal("unbound backup was accepted without a journal")
	}
	bug028Must(t, os.WriteFile(backup.caPath, newCA, 0o600))
	if _, err := preparePRDReplacementMacRouteServerTrust(mac, candidate, newCA, &journal, &backup.manifest, now); err == nil {
		t.Fatal("changed immutable backup was accepted even though live trust matched the target")
	}
}

func TestBUG028RouteTrustRetriesFromBackupAcrossEachFileInterruption(t *testing.T) {
	now := time.Now().UTC()
	oldCA, oldKey := prdReplacementTestCA(t, now)
	newCA, newKey := prdReplacementTestCA(t, now)
	beforeConfig := bug028SharedMacConfiguration(t)
	afterConfig, err := config.BuildFreshPRDReplacementMacRoute(beforeConfig)
	if err != nil {
		t.Fatal(err)
	}
	for _, interruption := range []int{1, 2, 3} {
		t.Run([]string{"after-known-hosts", "after-trust", "after-configuration"}[interruption-1], func(t *testing.T) {
			backup := bug028CABackup(t, oldCA)
			root := bug028PrivateTempDir(t)
			type change struct {
				path          string
				before, after []byte
			}
			merged, err := buildPRDReplacementMacServerTrust(beforeConfig, oldCA, newCA, now)
			if err != nil {
				t.Fatal(err)
			}
			changes := []change{
				{filepath.Join(root, "known_hosts"), []byte("retired host pin\n"), []byte("replacement host pin\n")},
				{filepath.Join(root, "shared-ca.pem"), oldCA, merged},
				{filepath.Join(root, "mac.yaml"), beforeConfig, afterConfig},
			}
			for _, item := range changes {
				bug028Must(t, os.WriteFile(item.path, item.before, 0o600))
			}
			for _, item := range changes[:interruption] {
				bug028Must(t, applyPRDReplacementMacRouteFile(item.path, digestBytes(item.before), digestBytes(item.after), item.after, 1<<20))
			}
			current, err := os.ReadFile(changes[1].path)
			bug028Must(t, err)
			journal := prdReplacementMacRouteJournal{PreviousServerCASHA256: digestBytes(oldCA), ServerCASHA256: digestBytes(merged),
				BackupDirectory: filepath.Dir(backup.manifest.DatabaseBackupPath)}
			retryTarget, err := preparePRDReplacementMacRouteServerTrust(beforeConfig, current, newCA, &journal, &backup.manifest, now)
			if err != nil || digestBytes(retryTarget) != digestBytes(merged) {
				t.Fatalf("backup-derived retry changed the recorded approved target: err=%v", err)
			}
			for _, item := range changes {
				bug028Must(t, applyPRDReplacementMacRouteFile(item.path, digestBytes(item.before), digestBytes(item.after), item.after, 1<<20))
			}
			installed, err := os.ReadFile(changes[1].path)
			if err != nil || !bytes.Equal(installed, merged) {
				t.Fatalf("retry changed approved shared trust: err=%v", err)
			}
			bug028RequireServerTrust(t, installed, bug028ServerCertificate(t, now, oldCA, oldKey, "132.226.205.205"), "132.226.205.205", now, true)
			bug028RequireServerTrust(t, installed, bug028ServerCertificate(t, now, newCA, newKey, "79.72.94.59"), "79.72.94.59", now, true)
			// Historical pre-repair backups remain unchanged, and cannot pass
			// validation as a backup of the newly approved union. A service
			// rollback must use evidence bound to the preserved trust union.
			if verifyPRDReplacementMacRouteBackupFiles(backup.manifest, map[string]string{backup.target: digestBytes(merged)}) == nil {
				t.Fatal("historical single-CA backup was accepted as the approved merged trust")
			}
			postRepair := bug028CABackup(t, merged)
			got, err := readPRDReplacementMacRouteServerCABackup(postRepair.manifest, postRepair.target, digestBytes(merged))
			if err != nil || !bytes.Equal(got, merged) {
				t.Fatalf("rollback evidence failed to preserve approved trust union: err=%v", err)
			}
		})
	}
}

func bug028SharedMacConfiguration(t *testing.T) []byte {
	t.Helper()
	peer := `  sandbox-host:
    account: ubuntu
    direct_endpoint:
      name: sandbox-prd
      url: https://132.226.205.205:8443
      server_ca: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/poc-ca.pem"
      client_certificate: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/direct-client.pem"
      client_private_key: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/direct-client.key"
  factory-sandbox:
    account: ubuntu
    direct_endpoint:
      name: factory-prd
      url: https://145.241.255.66:8443
      server_ca: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/poc-ca.pem"
      client_certificate: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/direct-client.pem"
      client_private_key: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/direct-client.key"
`
	contents := []byte(strings.Replace(prdReplacementBootstrapMacConfig, "mailboxes:\n", peer+"mailboxes:\n", 1))
	if _, err := config.ParseBytes(contents); err != nil {
		t.Fatalf("shared-trust Mac configuration fixture is invalid: %v", err)
	}
	return contents
}

func bug028ServerCertificate(t *testing.T, now time.Time, caPEM, keyPEM []byte, address string) []byte {
	t.Helper()
	ca := bug028ParseCertificate(t, caPEM)
	keyBlock, _ := pem.Decode(keyPEM)
	key, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(28), Subject: pkix.Name{CommonName: "BUG-028 peer fixture"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IPAddresses: []net.IP{net.ParseIP(address)},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &leafKey.PublicKey, key.(*ecdsa.PrivateKey))
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func bug028ParseCertificate(t *testing.T, raw []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(raw)
	if block == nil {
		t.Fatal("certificate fixture is not PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func bug028RequireServerTrust(t *testing.T, rootsPEM, leafPEM []byte, address string, now time.Time, want bool) {
	t.Helper()
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(rootsPEM) {
		t.Fatal("trust fixture contains no root")
	}
	_, err := bug028ParseCertificate(t, leafPEM).Verify(x509.VerifyOptions{
		Roots: roots, DNSName: address, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	if (err == nil) != want {
		t.Fatalf("IP-verified chain accepted=%t, want %t: %v", err == nil, want, err)
	}
}

type bug028BackupFixture struct {
	manifest PRDBackupManifest
	target   string
	caPath   string
}

func bug028CABackup(t *testing.T, contents []byte) bug028BackupFixture {
	t.Helper()
	root := bug028PrivateTempDir(t)
	secrets := filepath.Join(root, "secrets")
	bug028Must(t, os.Mkdir(secrets, 0o700))
	caPath := filepath.Join(secrets, "poc-ca.pem")
	bug028Must(t, os.WriteFile(caPath, contents, 0o600))
	db := filepath.Join(root, "state.db.online")
	bug028Must(t, os.WriteFile(db, []byte("immutable fixture database snapshot"), 0o600))
	target := "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/poc-ca.pem"
	return bug028BackupFixture{target: target, caPath: caPath, manifest: PRDBackupManifest{
		DatabaseBackupPath: db, DatabaseSHA256: digestFileSHA(db),
		Files: []PRDBackupFile{{TargetPath: target, BackupPath: "secrets/poc-ca.pem", SHA256: digestBytes(contents), Bytes: int64(len(contents)), Mode: 0o600}},
	}}
}

func bug028PrivateTempDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bug028Must(t, os.Chmod(root, 0o700))
	return root
}

func bug028Must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
