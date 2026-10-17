package laneinstall

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/sshdeploy"
)

var ErrInvalidQueuedSSHKey = errors.New("queued SSH plan requires one valid Ed25519 public key per identity")

type QueuedSSHPlan struct {
	HostID                     string
	Lane                       config.ReleaseLane
	PublicHost                 string
	Bind                       string
	Port                       int
	Namespace                  config.LaneNamespace
	HostKeyFingerprint         string
	DispatcherKeyFingerprint   string
	HostKeyPath                string
	AuthorizedKeysPath         string
	SSHDConfigPath             string
	ForcedWrapperPath          string
	RuntimeDirectory           string
	ClientKnownHostsPath       string
	ClientDispatcherPrivateKey string
	Artifacts                  []Artifact
}

type parsedSSHKey struct {
	keyType string
	keyData string
	blob    []byte
}

// BuildQueuedSSHPlan derives the fixed Linux listener and Mac client trust
// material for one remote profile and lane. It consumes public test/candidate
// identities only; it never reads, creates, or transports private keys.
func BuildQueuedSSHPlan(hostID string, lane config.ReleaseLane, dispatcherPublicKey, hostPublicKey string) (QueuedSSHPlan, error) {
	if lane != config.ReleaseLaneDev && lane != config.ReleaseLaneQAS {
		return QueuedSSHPlan{}, ErrInvalidLane
	}
	tuple, ok := canonicalHosts[hostID]
	if !ok || tuple.kind != config.HostKindLinux {
		return QueuedSSHPlan{}, ErrInvalidHost
	}
	dispatcherKey, err := parseEd25519PublicKey(dispatcherPublicKey)
	if err != nil {
		return QueuedSSHPlan{}, fmt.Errorf("dispatcher key: %w", err)
	}
	hostKey, err := parseEd25519PublicKey(hostPublicKey)
	if err != nil {
		return QueuedSSHPlan{}, fmt.Errorf("host key: %w", err)
	}

	ns, err := config.DeriveLaneNamespace(config.HostKindLinux, lane, hostID)
	if err != nil {
		return QueuedSSHPlan{}, fmt.Errorf("derive fixed queued SSH namespace: %w", err)
	}
	endpoint, err := url.Parse(ns.DirectEndpointURL)
	if err != nil || endpoint.Scheme != "https" || endpoint.Port() == "" || endpoint.Hostname() == "" {
		return QueuedSSHPlan{}, fmt.Errorf("derive public queued SSH host from fixed endpoint")
	}
	bindIP, _, err := net.SplitHostPort(ns.DirectHTTPSBind)
	if err != nil || net.ParseIP(bindIP) == nil {
		return QueuedSSHPlan{}, fmt.Errorf("derive private queued SSH bind from fixed endpoint")
	}
	port := ns.QueuePort
	if port != 2224 && port != 2225 {
		return QueuedSSHPlan{}, fmt.Errorf("unexpected fixed queued SSH port %d", port)
	}
	bind := net.JoinHostPort(bindIP, strconv.Itoa(port))
	authorityRoot := ns.SSHAuthorityPath
	if authorityRoot == "" || filepath.Clean(authorityRoot) != authorityRoot {
		return QueuedSSHPlan{}, fmt.Errorf("fixed queued SSH authority root is unavailable")
	}
	macNS, err := config.DeriveLaneNamespace(config.HostKindMac, lane, "")
	if err != nil {
		return QueuedSSHPlan{}, fmt.Errorf("derive fixed Mac lane namespace: %w", err)
	}
	macSSHRoot := filepath.Join(macNS.ServiceRoot, "secrets", "profiles", hostID, "ssh")
	serverHostKeyPath := filepath.Join(authorityRoot, "ssh_host_ed25519_key")
	authorizedKeysPath := filepath.Join(authorityRoot, "authorized_keys")
	sshdConfigPath := filepath.Join(authorityRoot, "sshd_config")
	forcedWrapperPath := filepath.Join(authorityRoot, "runner-ssh-bridge-forced.sh")
	runtimeDirectory := filepath.Join("/run", strings.TrimSuffix(ns.QueuedSSHUnit, ".service"))
	clientKnownHostsPath := filepath.Join(macSSHRoot, "known_hosts")
	clientPrivateKeyPath := filepath.Join(macSSHRoot, "dispatcher_ed25519")
	controllerMapPath := filepath.Join(ns.ServiceRoot, "config", "ssh-controller-map.yaml")
	bridgeBinaryPath := filepath.Join(ns.Binaries, "runner-ssh-bridge")

	hostFingerprint := lowerHexFingerprint(hostKey.blob)
	dispatcherFingerprint := lowerHexFingerprint(dispatcherKey.blob)
	serverSuppliedFingerprint := opensshFingerprint(dispatcherKey.blob)
	keyEntry, err := (sshdeploy.Entry{
		PublicKey:   dispatcherKey.keyType + " " + dispatcherKey.keyData,
		WrapperPath: forcedWrapperPath,
		Comment:     "runner-dispatcher-" + hostID + "-" + string(lane),
	}).Render()
	if err != nil {
		return QueuedSSHPlan{}, fmt.Errorf("render restricted dispatcher key: %w", err)
	}
	knownHost := fmt.Sprintf("[%s]:%d %s %s\n", endpoint.Hostname(), port, hostKey.keyType, hostKey.keyData)

	plan := QueuedSSHPlan{
		HostID: hostID, Lane: lane, PublicHost: endpoint.Hostname(), Bind: bind,
		Port: port, Namespace: ns, HostKeyFingerprint: hostFingerprint,
		DispatcherKeyFingerprint:   dispatcherFingerprint,
		HostKeyPath:                serverHostKeyPath,
		AuthorizedKeysPath:         authorizedKeysPath,
		SSHDConfigPath:             sshdConfigPath,
		ForcedWrapperPath:          forcedWrapperPath,
		RuntimeDirectory:           runtimeDirectory,
		ClientKnownHostsPath:       clientKnownHostsPath,
		ClientDispatcherPrivateKey: clientPrivateKeyPath,
	}
	plan.Artifacts = []Artifact{
		{Kind: ArtifactSSHConfig, InstallPath: sshdConfigPath, Mode: 0o600, Owner: "root", Group: "root", Contents: renderQueuedSSHDConfig(plan, serverSuppliedFingerprint)},
		{Kind: ArtifactSSHKeys, InstallPath: authorizedKeysPath, Mode: 0o600, Owner: "root", Group: "root", Contents: keyEntry + "\n"},
		{Kind: ArtifactSSHWrapper, InstallPath: forcedWrapperPath, Mode: 0o755, Owner: "root", Group: "root", Contents: renderQueuedSSHForcedWrapper(plan, serverSuppliedFingerprint, bridgeBinaryPath, controllerMapPath)},
		{Kind: ArtifactSSHUnit, InstallPath: filepath.Join("/etc/systemd/system", ns.QueuedSSHUnit), Mode: 0o644, Owner: "root", Group: "root", Contents: renderQueuedSSHUnit(plan)},
		{Kind: ArtifactSSHKnownHosts, InstallPath: clientKnownHostsPath, Mode: 0o600, Owner: config.MacAccount, Group: "staff", Contents: knownHost},
	}
	return plan, nil
}

func parseEd25519PublicKey(raw string) (parsedSSHKey, error) {
	if strings.ContainsAny(raw, "\r\n") {
		return parsedSSHKey{}, ErrInvalidQueuedSSHKey
	}
	fields := strings.Fields(raw)
	if (len(fields) != 2 && len(fields) != 3) || fields[0] != "ssh-ed25519" {
		return parsedSSHKey{}, ErrInvalidQueuedSSHKey
	}
	blob, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil {
		return parsedSSHKey{}, ErrInvalidQueuedSSHKey
	}
	if len(blob) < 4 {
		return parsedSSHKey{}, ErrInvalidQueuedSSHKey
	}
	keyTypeLength := int(binary.BigEndian.Uint32(blob[:4]))
	if keyTypeLength != len("ssh-ed25519") || len(blob) < 4+keyTypeLength+4 {
		return parsedSSHKey{}, ErrInvalidQueuedSSHKey
	}
	keyType := string(blob[4 : 4+keyTypeLength])
	keyLengthOffset := 4 + keyTypeLength
	keyLength := int(binary.BigEndian.Uint32(blob[keyLengthOffset : keyLengthOffset+4]))
	if keyType != "ssh-ed25519" || keyLength != 32 || len(blob) != keyLengthOffset+4+keyLength {
		return parsedSSHKey{}, ErrInvalidQueuedSSHKey
	}
	return parsedSSHKey{keyType: keyType, keyData: fields[1], blob: blob}, nil
}

func lowerHexFingerprint(blob []byte) string {
	digest := sha256.Sum256(blob)
	return fmt.Sprintf("sha256:%x", digest)
}

func opensshFingerprint(blob []byte) string {
	digest := sha256.Sum256(blob)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(digest[:])
}

func renderQueuedSSHDConfig(plan QueuedSSHPlan, dispatcherFingerprint string) string {
	return fmt.Sprintf(`# Dev/QAS queued bridge. This file is standalone and has no Include directive.
AddressFamily inet
Port %d
ListenAddress %s
HostKey %s
PidFile %s
UsePAM no
PermitRootLogin no
AllowUsers ubuntu
AuthenticationMethods publickey
PubkeyAuthentication yes
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitEmptyPasswords no
AuthorizedKeysFile %s
StrictModes yes
PermitTTY no
AllowAgentForwarding no
AllowTcpForwarding no
AllowStreamLocalForwarding no
GatewayPorts no
X11Forwarding no
PermitTunnel no
PermitUserEnvironment no
PermitUserRC no
HostbasedAuthentication no
GSSAPIAuthentication no
LoginGraceTime 30s
MaxAuthTries 3
MaxSessions 1
MaxStartups 10:30:30
UseDNS no
PrintMotd no
PrintLastLog no
SyslogFacility AUTHPRIV
LogLevel VERBOSE

Match User ubuntu
	ForceCommand %s %s
`, plan.Port, plan.Bind, plan.HostKeyPath, filepath.Join(plan.RuntimeDirectory, "sshd.pid"), plan.AuthorizedKeysPath, plan.ForcedWrapperPath, dispatcherFingerprint)
}

func renderQueuedSSHForcedWrapper(plan QueuedSSHPlan, fingerprint, bridgeBinaryPath, controllerMapPath string) string {
	const template = `#!/bin/sh
set -eu

BRIDGE_BIN=@BRIDGE_BIN@
CONTROLLER_MAP=@CONTROLLER_MAP@
RUNNERD_SOCKET=@RUNNERD_SOCKET@
EXPECTED_COMMAND='runner-ssh-bridge --stdio'
EXPECTED_FINGERPRINT=@FINGERPRINT@

fail() {
	printf 'runner SSH bridge: %s\n' "$1" >&2
	exit 126
}

if [ "$#" -ne 1 ] || [ "$1" != "$EXPECTED_FINGERPRINT" ]; then
	fail 'authenticated key identity is not permitted'
fi
case "$EXPECTED_FINGERPRINT" in
	SHA256:*) ;;
	*) fail 'authenticated key identity is not permitted' ;;
esac
if [ "${SSH_ORIGINAL_COMMAND-}" != "$EXPECTED_COMMAND" ]; then
	fail 'requested command is not permitted'
fi
if [ -n "${SSH_TTY-}" ]; then
	fail 'PTY is not permitted'
fi

exec "$BRIDGE_BIN" --stdio --authenticated-key "$EXPECTED_FINGERPRINT" --controller-map "$CONTROLLER_MAP" --runnerd-socket "$RUNNERD_SOCKET"
`
	replacer := strings.NewReplacer(
		"@BRIDGE_BIN@", shellQuote(bridgeBinaryPath),
		"@CONTROLLER_MAP@", shellQuote(controllerMapPath),
		"@RUNNERD_SOCKET@", shellQuote(plan.Namespace.PrivateSocket),
		"@FINGERPRINT@", shellQuote(fingerprint),
	)
	return replacer.Replace(template)
}

func renderQueuedSSHUnit(plan QueuedSSHPlan) string {
	return fmt.Sprintf(`[Unit]
Description=Remote Session Runner %s lane queued SSH bridge
Wants=network-online.target
After=network-online.target
StartLimitIntervalSec=60s
StartLimitBurst=3

[Service]
Type=simple
User=root
Group=root
UMask=0077
ExecStart=/usr/sbin/sshd -D -e -f %s
RuntimeDirectory=%s
RuntimeDirectoryMode=0755
Restart=on-failure
RestartSec=3s
KillMode=control-group
TimeoutStopSec=30s
StandardOutput=null
StandardError=null
SyslogIdentifier=%s
Slice=%s

[Install]
WantedBy=multi-user.target
`, plan.Lane, plan.SSHDConfigPath, strings.TrimPrefix(plan.RuntimeDirectory, "/run/"), plan.Namespace.QueuedSSHJournal, plan.Namespace.RunnerSlice)
}
