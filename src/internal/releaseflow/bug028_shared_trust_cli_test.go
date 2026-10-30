package releaseflow

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
	"testing"
	"time"

	"remote-session-runner/src/internal/laneinstall"
)

type bug028CLISharedTrust struct {
	original, replacement, replacementKey, peerCertificate []byte
	backup, installed, unrelatedKey                        string
}

func newBUG028CLISharedTrust(t *testing.T, now time.Time) bug028CLISharedTrust {
	t.Helper()
	makeCA := func() ([]byte, []byte, *x509.Certificate, *ecdsa.PrivateKey) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
		if err != nil {
			t.Fatal(err)
		}
		template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "BUG028 public-command fixture CA"},
			NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), IsCA: true,
			BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
		der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		certificate, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		keyDER, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
			pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), certificate, key
	}
	original, _, oldRoot, oldKey := makeCA()
	replacement, replacementKey, _, _ := makeCA()
	peerKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	peer := &x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("132.226.205.205")}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		KeyUsage: x509.KeyUsageDigitalSignature}
	peerDER, err := x509.CreateCertificate(rand.Reader, peer, oldRoot, &peerKey.PublicKey, oldKey)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	fixture := bug028CLISharedTrust{original: original, replacement: replacement, replacementKey: replacementKey,
		peerCertificate: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: peerDER}),
		backup:          filepath.Join(root, "original-ca.backup.pem"), installed: filepath.Join(root, "shared-ca.pem"),
		unrelatedKey: filepath.Join(root, "unrelated-key")}
	for path, contents := range map[string][]byte{fixture.backup: original, fixture.installed: original, fixture.unrelatedKey: []byte("unchanged fixture key")} {
		if err := os.WriteFile(path, contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return fixture
}

// The public CLI retains its test-only external service backend, but its
// Mac-route callback now invokes the real production trust planner. Native
// immutable-backup/CAS interruption guards are exercised in laneinstall.
func (f bug028CLISharedTrust) apply(input laneinstall.PRDReplacementMacRouteInput, now time.Time) (string, error) {
	original, err := os.ReadFile(f.backup)
	if err != nil || !bytes.Equal(original, f.original) {
		return "", errors.New("fixture immutable CA backup changed")
	}
	candidate, err := laneinstall.BuildPRDReplacementMacServerTrust([]byte(bug028CLISharedMacConfiguration), original, input.ServerCAPEM, now)
	if err != nil {
		return "", err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(candidate) {
		return "", errors.New("fixture candidate trust is invalid")
	}
	for ip, leaf := range map[string][]byte{"132.226.205.205": f.peerCertificate, "79.72.94.59": input.ServerCertificatePEM} {
		block, _ := pem.Decode(leaf)
		if block == nil {
			return "", errors.New("fixture public certificate missing")
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return "", err
		}
		if _, err := certificate.Verify(x509.VerifyOptions{Roots: roots, DNSName: ip, CurrentTime: now}); err != nil {
			return "", err
		}
	}
	current, err := os.ReadFile(f.installed)
	if err != nil || !bytes.Equal(current, original) && !bytes.Equal(current, candidate) {
		return "", errors.New("fixture installed trust drifted")
	}
	if err := os.WriteFile(f.installed, candidate, 0o600); err != nil {
		return "", err
	}
	key, err := os.ReadFile(f.unrelatedKey)
	if err != nil || string(key) != "unchanged fixture key" {
		return "", errors.New("fixture unrelated key changed")
	}
	return digest(candidate), nil
}

const bug028CLISharedMacConfiguration = `version: 2
mac:
  account: tomasz.walczuk
  service_root: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner"
  api_socket: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/run/local-api.sock"
  locald_socket: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/run/locald.sock"
  sqlite: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/state/local.db"
  workspaces: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/workspaces"
  script_temp_root: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/tmp/scripts"
  backups: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/backups"
environment_registry:
  mac-dev:
    base_system: macOS
    host_class: macOS workstation
    effective_account: tomasz.walczuk
    allowed_targets: [{kind: local, profile: mac-workstation}]
    allowed_source_modes: [empty]
    allowed_repository_aliases: []
    allowed_controllers: [{type: local_user, id: tomasz.walczuk}]
  linux-dev:
    base_system: Ubuntu 22.04 LTS
    host_class: Ubuntu Linux host
    effective_account: ubuntu
    allowed_targets: [{kind: remote, profile: linux-host}]
    allowed_source_modes: [empty]
    allowed_repository_aliases: []
    allowed_controllers: [{type: queued_mac, id: tomasz.walczuk}, {type: direct_mtls, id: tomasz.walczuk}]
execution_contexts:
  mac-local: {environment: mac-dev, execution_target: {kind: local, profile: mac-workstation}}
  ubuntu-current: {environment: linux-dev, execution_target: {kind: remote, profile: linux-host}}
remote_hosts:
  linux-host:
    account: ubuntu
    queued_bridge:
      host: 129.151.232.40
      port: 22
      known_hosts: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/ssh_known_hosts"
      private_key: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/dispatcher_ed25519"
    direct_endpoint:
      name: linux-poc
      url: https://129.151.232.40:8443
      server_ca: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/poc-ca.pem"
      client_certificate: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/direct-client.pem"
      client_private_key: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/direct-client.key"
  sandbox-host:
    account: ubuntu
    direct_endpoint:
      name: sandbox-poc
      url: https://132.226.205.205:8443
      server_ca: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/poc-ca.pem"
      client_certificate: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/sandbox-client.pem"
      client_private_key: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/sandbox-client.key"
mailboxes:
  default:
    root: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/mailbox"
    repository_aliases: [remote-session-runner]
    default_execution: mac-local
    allowed_execution: [mac-local, ubuntu-current]
`
