package laneinstall

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/sys/unix"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/sshdeploy"
)

type SimpleQueuedSSHPlan struct {
	HostID                       string
	Lane                         config.ReleaseLane
	Profile                      string
	PublicHost                   string
	Port                         int
	HostKeyFingerprint           string
	DispatcherKeyFingerprint     string
	DispatcherOpenSSHFingerprint string
	HostKeyPublicPath            string
	AuthorizedKeysPath           string
	ForcedWrapperPath            string
	ControllerMapPath            string
	ClientKnownHostsPath         string
	ClientDispatcherPrivateKey   string
	LinuxArtifacts               []Artifact
	MacArtifacts                 []Artifact
	entry                        sshdeploy.Entry
	linuxArtifactsHash           [3]string
	macArtifactHash              string
}

// BuildSimpleQueuedSSHPlan binds one isolated lane key to a restricted command
// on the host's existing OpenSSH listener. It consumes public keys only and
// never allocates another listener, sshd unit, root authority, or port.
func BuildSimpleQueuedSSHPlan(hostID string, lane config.ReleaseLane, dispatcherPublicKey, hostPublicKey string) (SimpleQueuedSSHPlan, error) {
	linuxPlan, err := BuildSimplePlan(hostID, lane)
	if err != nil || linuxPlan.Namespace.Host != config.HostKindLinux {
		return SimpleQueuedSSHPlan{}, ErrInvalidHost
	}
	return buildSimpleQueuedSSHPlan(hostID, lane, dispatcherPublicKey, hostPublicKey, linuxPlan.Namespace.DirectEndpointURL)
}

// BuildSimpleQueuedSSHPlanFromV3Profiles binds known_hosts to the endpoint
// explicitly selected by matching Mac and Linux V3 configs. Pair validation
// requires the same URL, queue host, controller pins, TLS pins, and lane SANs;
// no address can be introduced as a command-line override.
func BuildSimpleQueuedSSHPlanFromV3Profiles(hostID string, lane config.ReleaseLane, dispatcherPublicKey, hostPublicKey string, mac, linux config.ValidatedV3Profiles) (SimpleQueuedSSHPlan, error) {
	if mac.Lane() != lane || linux.Lane() != lane ||
		config.ValidateV3MacLinuxProfilePair(mac, linux, hostID) != nil {
		return SimpleQueuedSSHPlan{}, config.ErrInvalidLaneProfiles
	}
	profile, ok := mac.MacProfile(hostID)
	linuxProfile, linuxOK := linux.LinuxProfileBinding()
	if !ok || !linuxOK || linux.RemoteProfile() != hostID ||
		profile.Queued.Host != linuxProfile.Queued.Host || profile.Direct.URL != linuxProfile.Direct.URL {
		return SimpleQueuedSSHPlan{}, config.ErrInvalidLaneProfiles
	}
	publicURL := profile.Direct.URL
	endpoint, err := url.Parse(publicURL)
	if err != nil || endpoint.Scheme != "https" || endpoint.User != nil || endpoint.Path != "" ||
		endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Port() == "" ||
		!config.SimpleLanePublicRouteAddressAllowedForLane(hostID, lane, endpoint.Hostname()) {
		return SimpleQueuedSSHPlan{}, config.ErrInvalidLaneProfiles
	}
	return buildSimpleQueuedSSHPlan(hostID, lane, dispatcherPublicKey, hostPublicKey, publicURL)
}

func buildSimpleQueuedSSHPlan(hostID string, lane config.ReleaseLane, dispatcherPublicKey, hostPublicKey, publicURL string) (SimpleQueuedSSHPlan, error) {
	linuxPlan, err := BuildSimplePlan(hostID, lane)
	if err != nil || linuxPlan.Namespace.Host != config.HostKindLinux {
		return SimpleQueuedSSHPlan{}, ErrInvalidHost
	}
	dispatcher, err := parseEd25519PublicKey(dispatcherPublicKey)
	if err != nil {
		return SimpleQueuedSSHPlan{}, fmt.Errorf("dispatcher key: %w", err)
	}
	hostKey, err := parseEd25519PublicKey(hostPublicKey)
	if err != nil {
		return SimpleQueuedSSHPlan{}, fmt.Errorf("existing SSH host key: %w", err)
	}
	macNamespace, err := config.DeriveSimpleLaneNamespace(config.HostKindMac, lane, "")
	if err != nil {
		return SimpleQueuedSSHPlan{}, err
	}
	endpoint, err := url.Parse(publicURL)
	if err != nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" || endpoint.User != nil ||
		endpoint.Path != "" || endpoint.RawQuery != "" || endpoint.Fragment != "" ||
		!config.SimpleLanePublicRouteAddressAllowedForLane(hostID, lane, endpoint.Hostname()) ||
		endpoint.Port() != fmt.Sprint(linuxPlan.Namespace.DirectPort) || linuxPlan.Namespace.QueuePort != 22 || linuxPlan.Namespace.QueuedSSHUnit != "" {
		return SimpleQueuedSSHPlan{}, ErrInvalidSimpleBundle
	}
	wrapperPath := filepath.Join(linuxPlan.Namespace.Binaries, "runner-ssh-bridge-forced.sh")
	controllerMapPath := filepath.Join(linuxPlan.Namespace.ServiceRoot, "config", "ssh-controller-map.yaml")
	authorizedKeysPath := filepath.Join("/home", config.LinuxAccount, ".ssh", "authorized_keys")
	knownHostsPath := filepath.Join(macNamespace.ServiceRoot, "secrets", "profiles", hostID, "ssh", "known_hosts")
	dispatcherPrivatePath := filepath.Join(macNamespace.ServiceRoot, "secrets", "profiles", hostID, "ssh", "dispatcher_ed25519")
	entry := sshdeploy.Entry{
		PublicKey:   dispatcher.keyType + " " + dispatcher.keyData,
		WrapperPath: wrapperPath,
		Comment:     "runner-dispatcher-" + hostID + "-" + string(lane),
	}
	keyLine, err := entry.Render()
	if err != nil {
		return SimpleQueuedSSHPlan{}, err
	}
	openSSHFingerprint := opensshFingerprint(dispatcher.blob)
	hostFingerprint := lowerHexFingerprint(hostKey.blob)
	dispatcherFingerprint := lowerHexFingerprint(dispatcher.blob)
	controllerMap := fmt.Sprintf("version: 1\nkeys:\n  %q:\n    controller_type: %s\n    controller_id: %q\n", openSSHFingerprint, domain.ControllerTypeQueuedMac, config.MacAccount)
	publicHost := endpoint.Hostname()
	// OpenSSH matches the bare host token when connecting on its default port,
	// including when the caller explicitly supplies -p 22. Bracketed
	// [host]:port entries are for non-default ports and do not match this route.
	knownHost := fmt.Sprintf("%s %s %s\n", publicHost, hostKey.keyType, hostKey.keyData)
	plan := SimpleQueuedSSHPlan{
		HostID: hostID, Lane: lane, Profile: hostID, PublicHost: publicHost, Port: 22,
		HostKeyFingerprint: hostFingerprint, DispatcherKeyFingerprint: dispatcherFingerprint,
		DispatcherOpenSSHFingerprint: openSSHFingerprint,
		HostKeyPublicPath:            linuxPlan.Namespace.SSHHostPublicKeyFile, AuthorizedKeysPath: authorizedKeysPath,
		ForcedWrapperPath: wrapperPath, ControllerMapPath: controllerMapPath,
		ClientKnownHostsPath: knownHostsPath, ClientDispatcherPrivateKey: dispatcherPrivatePath, entry: entry,
	}
	plan.LinuxArtifacts = []Artifact{
		{Kind: ArtifactSSHWrapper, InstallPath: wrapperPath, Mode: 0o700, Owner: config.LinuxAccount, Group: config.LinuxAccount, Contents: renderSimpleQueuedSSHWrapper(linuxPlan.Namespace, wrapperPath, controllerMapPath, openSSHFingerprint)},
		{Kind: ArtifactSSHControllerMap, InstallPath: controllerMapPath, Mode: 0o600, Owner: config.LinuxAccount, Group: config.LinuxAccount, Contents: controllerMap},
		{Kind: ArtifactSSHKeys, InstallPath: authorizedKeysPath, Mode: 0o600, Owner: config.LinuxAccount, Group: config.LinuxAccount, Contents: keyLine + "\n"},
	}
	plan.MacArtifacts = []Artifact{
		{Kind: ArtifactSSHKnownHosts, InstallPath: knownHostsPath, Mode: 0o600, Owner: config.MacAccount, Group: "staff", Contents: knownHost},
	}
	for index, artifact := range plan.LinuxArtifacts {
		plan.linuxArtifactsHash[index] = simpleArtifactHash(artifact)
	}
	plan.macArtifactHash = simpleArtifactHash(plan.MacArtifacts[0])
	if !simpleQueuedSSHPlanValid(plan) {
		return SimpleQueuedSSHPlan{}, ErrInvalidSimpleBundle
	}
	return plan, nil
}

func renderSimpleQueuedSSHWrapper(namespace config.LaneNamespace, wrapper, controllerMap, fingerprint string) string {
	bridge := filepath.Join(namespace.ServiceRoot, "current", "bin", "runner-ssh-bridge")
	const template = `#!/bin/sh
set -eu

EXPECTED_FINGERPRINT=@FINGERPRINT@
EXPECTED_COMMAND='runner-ssh-bridge --stdio'
BRIDGE_BIN=@BRIDGE@
BRIDGE_HOST=@HOST@
BRIDGE_LANE=@LANE@
CONTROLLER_MAP=@MAP@
RUNNERD_SOCKET=@SOCKET@

fail() {
	printf 'runner SSH bridge: %s\n' "$1" >&2
	exit 126
}

if [ "$#" -ne 1 ] || [ "$1" != "$EXPECTED_FINGERPRINT" ]; then
	fail 'authenticated key identity is not permitted'
fi
case "$EXPECTED_FINGERPRINT" in SHA256:*) ;; *) fail 'authenticated key identity is not permitted' ;; esac
if [ "${SSH_ORIGINAL_COMMAND-}" != "$EXPECTED_COMMAND" ]; then
	fail 'requested command is not permitted'
fi
if [ -n "${SSH_TTY-}" ]; then
	fail 'PTY is not permitted'
fi
exec "$BRIDGE_BIN" --stdio --authenticated-key "$EXPECTED_FINGERPRINT" --controller-map "$CONTROLLER_MAP" --runnerd-socket "$RUNNERD_SOCKET" --catalog-host "$BRIDGE_HOST" --catalog-lane "$BRIDGE_LANE"
`
	replacer := strings.NewReplacer(
		"@FINGERPRINT@", shellQuote(fingerprint),
		"@BRIDGE@", shellQuote(bridge),
		"@HOST@", shellQuote(namespace.RemoteProfile),
		"@LANE@", shellQuote(string(namespace.Lane)),
		"@MAP@", shellQuote(controllerMap),
		"@SOCKET@", shellQuote(namespace.PrivateSocket),
	)
	return replacer.Replace(template)
}

func (p SimpleQueuedSSHPlan) ValidateProfiles(mac, linux config.ValidatedV3Profiles) error {
	if p.HostID == "" || p.Profile != p.HostID || p.Port != 22 || mac.Host() != config.HostKindMac ||
		linux.Host() != config.HostKindLinux || mac.Lane() != p.Lane || linux.Lane() != p.Lane || linux.RemoteProfile() != p.Profile {
		return config.ErrInvalidLaneProfiles
	}
	macRoute, ok := mac.MacProfile(p.Profile)
	if !ok {
		return config.ErrInvalidLaneProfiles
	}
	linuxRoute, ok := linux.LinuxProfileBinding()
	if !ok || macRoute.Queued.Port != 22 || macRoute.Queued.Host != p.PublicHost ||
		macRoute.Queued.KnownHostsFile != p.ClientKnownHostsPath || macRoute.Queued.DispatcherPrivateKeyFile != p.ClientDispatcherPrivateKey ||
		macRoute.Queued.HostKeyFingerprint != p.HostKeyFingerprint || macRoute.Queued.DispatcherPublicKeyFingerprint != p.DispatcherKeyFingerprint ||
		linuxRoute.Queued.Port != 22 || linuxRoute.Queued.HostKeyFingerprint != p.HostKeyFingerprint ||
		linuxRoute.Queued.AuthorizedKeyFingerprint != p.DispatcherKeyFingerprint {
		return config.ErrInvalidLaneProfiles
	}
	return config.ValidateV3MacLinuxProfilePair(mac, linux, p.Profile)
}

// InstallSimpleQueuedSSHArtifacts writes only the selected Mac known_hosts
// entry or the Linux lane wrapper/controller map plus one restricted public
// key. It never changes sshd, the shared listener, or a Runner service.
func InstallSimpleQueuedSSHArtifacts(plan SimpleQueuedSSHPlan, localHostID string) error {
	if !simpleQueuedSSHPlanValid(plan) {
		return ErrSimpleArtifactConflict
	}
	current, err := user.Current()
	if err != nil {
		return err
	}
	if localHostID == "mac-control-plane" {
		if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" || current.Username != config.MacAccount || len(plan.MacArtifacts) != 1 {
			return ErrSimpleServiceTarget
		}
		macNamespace, err := config.DeriveSimpleLaneNamespace(config.HostKindMac, plan.Lane, "")
		if err != nil {
			return err
		}
		profileSSH := filepath.Join(macNamespace.ServiceRoot, "secrets", "profiles", plan.Profile, "ssh")
		if err := ensureSimpleOwnedDirectory(filepath.Join(macNamespace.ServiceRoot, "secrets")); err != nil {
			return err
		}
		if err := ensureSimpleOwnedDirectory(filepath.Join(macNamespace.ServiceRoot, "secrets", "profiles")); err != nil {
			return err
		}
		if err := ensureSimpleOwnedDirectory(filepath.Join(macNamespace.ServiceRoot, "secrets", "profiles", plan.Profile)); err != nil {
			return err
		}
		if err := ensureSimpleOwnedDirectory(profileSSH); err != nil {
			return err
		}
		return writeSimpleUserArtifact(plan.MacArtifacts[0])
	}
	linux, err := BuildSimplePlan(localHostID, plan.Lane)
	if err != nil || runtime.GOOS != "linux" || runtime.GOARCH != simpleHostPlatforms[localHostID].goarch ||
		current.Username != config.LinuxAccount || localHostID != plan.HostID || linux.Namespace.Host != config.HostKindLinux {
		return ErrSimpleServiceTarget
	}
	for _, directory := range []string{linux.Namespace.ServiceRoot, linux.Namespace.Binaries, filepath.Join(linux.Namespace.ServiceRoot, "config"), linux.Namespace.Backups} {
		if err := ensureSimpleOwnedDirectory(directory); err != nil {
			return err
		}
	}
	for _, artifact := range plan.LinuxArtifacts[:2] {
		if err := writeSimpleUserArtifact(artifact); err != nil {
			return err
		}
	}
	return appendSimpleAuthorizedKeyAt(plan.AuthorizedKeysPath, plan.LinuxArtifacts[2].Contents, linux.Namespace.Backups)
}

func simpleQueuedSSHPlanValid(plan SimpleQueuedSSHPlan) bool {
	linux, err := BuildSimplePlan(plan.HostID, plan.Lane)
	if err != nil || linux.Namespace.Host != config.HostKindLinux || plan.Port != 22 ||
		plan.AuthorizedKeysPath != filepath.Join("/home", config.LinuxAccount, ".ssh", "authorized_keys") ||
		plan.HostKeyPublicPath != linux.Namespace.SSHHostPublicKeyFile ||
		plan.ForcedWrapperPath != filepath.Join(linux.Namespace.Binaries, "runner-ssh-bridge-forced.sh") ||
		plan.ControllerMapPath != filepath.Join(linux.Namespace.ServiceRoot, "config", "ssh-controller-map.yaml") {
		return false
	}
	mac, err := config.DeriveSimpleLaneNamespace(config.HostKindMac, plan.Lane, "")
	if err != nil || plan.ClientKnownHostsPath != filepath.Join(mac.ServiceRoot, "secrets", "profiles", plan.HostID, "ssh", "known_hosts") ||
		plan.ClientDispatcherPrivateKey != filepath.Join(mac.ServiceRoot, "secrets", "profiles", plan.HostID, "ssh", "dispatcher_ed25519") {
		return false
	}
	if len(plan.LinuxArtifacts) != 3 || len(plan.MacArtifacts) != 1 {
		return false
	}
	if !config.SimpleLanePublicRouteAddressAllowedForLane(plan.HostID, plan.Lane, plan.PublicHost) ||
		!strings.HasPrefix(plan.MacArtifacts[0].Contents, plan.PublicHost+" ssh-ed25519 ") {
		return false
	}
	for index, artifact := range plan.LinuxArtifacts {
		if artifact.Contents == "" || simpleArtifactHash(artifact) != plan.linuxArtifactsHash[index] {
			return false
		}
	}
	return plan.LinuxArtifacts[0].Kind == ArtifactSSHWrapper && plan.LinuxArtifacts[0].Mode == 0o700 &&
		plan.LinuxArtifacts[0].InstallPath == plan.ForcedWrapperPath && plan.LinuxArtifacts[0].Owner == config.LinuxAccount &&
		plan.LinuxArtifacts[1].Kind == ArtifactSSHControllerMap && plan.LinuxArtifacts[1].Mode == 0o600 &&
		plan.LinuxArtifacts[1].InstallPath == plan.ControllerMapPath && plan.LinuxArtifacts[1].Owner == config.LinuxAccount &&
		plan.LinuxArtifacts[2].Kind == ArtifactSSHKeys && plan.LinuxArtifacts[2].Mode == 0o600 &&
		plan.LinuxArtifacts[2].InstallPath == plan.AuthorizedKeysPath && plan.LinuxArtifacts[2].Owner == config.LinuxAccount &&
		plan.MacArtifacts[0].Kind == ArtifactSSHKnownHosts && plan.MacArtifacts[0].Mode == 0o600 &&
		plan.MacArtifacts[0].InstallPath == plan.ClientKnownHostsPath && plan.macArtifactHash == simpleArtifactHash(plan.MacArtifacts[0])
}

func appendSimpleAuthorizedKeyAt(path, line, backupDirectory string) error {
	if filepath.Clean(path) != path || !filepath.IsAbs(path) ||
		backupDirectory == "" || !strings.HasSuffix(line, "\n") || strings.Contains(line, "\r") || strings.Count(line, "\n") != 1 {
		return ErrSimpleArtifactConflict
	}
	parent := filepath.Dir(path)
	var parentInfo unix.Stat_t
	if err := unix.Lstat(parent, &parentInfo); err != nil || parentInfo.Mode&unix.S_IFMT != unix.S_IFDIR ||
		parentInfo.Uid != uint32(os.Getuid()) || parentInfo.Mode&0o077 != 0 {
		return ErrSimpleArtifactConflict
	}
	lockPath := path + ".runner-lane-lock"
	lockFD, err := unix.Open(lockPath, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	defer unix.Close(lockFD)
	var lockInfo unix.Stat_t
	if err := unix.Fstat(lockFD, &lockInfo); err != nil || lockInfo.Mode&unix.S_IFMT != unix.S_IFREG || lockInfo.Uid != uint32(os.Getuid()) || lockInfo.Mode&0o777 != 0o600 {
		return ErrSimpleArtifactConflict
	}
	var namedLock unix.Stat_t
	if err := unix.Lstat(lockPath, &namedLock); err != nil || namedLock.Mode&unix.S_IFMT != unix.S_IFREG || namedLock.Dev != lockInfo.Dev || namedLock.Ino != lockInfo.Ino {
		return ErrSimpleArtifactConflict
	}
	if err := unix.Flock(lockFD, unix.LOCK_EX); err != nil {
		return err
	}
	defer unix.Flock(lockFD, unix.LOCK_UN)
	before, err := readAuthorizedKeys(path)
	if err != nil {
		return err
	}
	keyFields := strings.Fields(strings.TrimSuffix(line, "\n"))
	keyTypeIndex := -1
	for index, field := range keyFields {
		if field == "ssh-ed25519" {
			keyTypeIndex = index
			break
		}
	}
	if keyTypeIndex < 0 || keyTypeIndex+1 >= len(keyFields) {
		return ErrSimpleArtifactConflict
	}
	keyData := keyFields[keyTypeIndex+1]
	for _, existing := range strings.Split(string(before), "\n") {
		fields := strings.Fields(existing)
		for index := 0; index+1 < len(fields); index++ {
			if fields[index] == "ssh-ed25519" && fields[index+1] == keyData {
				if existing == strings.TrimSuffix(line, "\n") {
					return nil
				}
				return ErrSimpleArtifactConflict
			}
		}
	}
	backupHash := sha256.Sum256(before)
	backupPath := filepath.Join(backupDirectory, "authorized-keys-"+hex.EncodeToString(backupHash[:])+".bak")
	if len(before) > 0 {
		if err := writeSimpleBackupFile(backupPath, before); err != nil {
			return err
		}
	}
	after := append([]byte(nil), before...)
	if len(after) > 0 && after[len(after)-1] != '\n' {
		after = append(after, '\n')
	}
	after = append(after, line...)
	if err := replaceAuthorizedKeys(path, before, after); err != nil {
		return err
	}
	return nil
}

func readAuthorizedKeys(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return nil, ErrSimpleArtifactConflict
	}
	var stat unix.Stat_t
	if err := unix.Lstat(path, &stat); err != nil || stat.Uid != uint32(os.Getuid()) {
		return nil, ErrSimpleArtifactConflict
	}
	return os.ReadFile(path)
}

func writeSimpleBackupFile(path string, contents []byte) error {
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			return ErrSimpleArtifactConflict
		}
		var stat unix.Stat_t
		if err := unix.Lstat(path, &stat); err != nil || stat.Uid != uint32(os.Getuid()) {
			return ErrSimpleArtifactConflict
		}
		existing, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if string(existing) == string(contents) {
			return nil
		}
		return ErrSimpleArtifactConflict
	} else if !os.IsNotExist(err) {
		return err
	}
	return writePrivateFile(path, contents, 0o600)
}

func replaceAuthorizedKeys(path string, before, after []byte) error {
	current, err := readAuthorizedKeys(path)
	if err != nil || string(current) != string(before) {
		return ErrSimpleArtifactConflict
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".authorized_keys.runner-lane-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(after); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return syncSimpleDirectory(filepath.Dir(path))
}

func simpleArtifactHash(artifact Artifact) string {
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d\x00%s\x00%s\x00%s", artifact.Kind, artifact.InstallPath, artifact.Mode, artifact.Owner, artifact.Group, artifact.Contents)))
	return hex.EncodeToString(hash[:])
}
