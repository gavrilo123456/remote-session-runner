package laneinstall

import (
	"fmt"
	"path/filepath"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/logcapture"
)

// BuildPRDCatalogServiceArtifacts renders the host-specific launch artifacts
// needed to move the unsuffixed service from regular bin files to its sealed
// catalog selector. It does not write files, stop services, or change PRD.
// The existing service identities, config and writable paths are preserved.
func BuildPRDCatalogServiceArtifacts(hostID string) ([]Artifact, error) {
	plan, err := BuildCatalogPlan(hostID, config.ReleaseLanePRD)
	if err != nil {
		return nil, err
	}
	ns := plan.Namespace
	currentBin := filepath.Join(ns.ServiceRoot, "current", "bin")
	if ns.Host == config.HostKindMac {
		limits := simpleLaneLogCaptureArgs()
		return []Artifact{
			{Kind: ArtifactLaunchAgent, InstallPath: ns.LocalAgentPlist, Mode: 0o644, Contents: renderLaunchAgent(ns.LocalAgentLabel, ns.ServiceRoot,
				filepath.Join(currentBin, "runner-log-capture"), filepath.Join(ns.Logs, "local.log"), filepath.Join(currentBin, "runner-local"), ns.ConfigFile, limits...)},
			{Kind: ArtifactLaunchAgent, InstallPath: ns.LocalDAgentPlist, Mode: 0o644, Contents: renderLaunchAgent(ns.LocalDAgentLabel, ns.ServiceRoot,
				filepath.Join(currentBin, "runner-log-capture"), filepath.Join(ns.Logs, "locald.log"), filepath.Join(currentBin, "runner-locald"), ns.ConfigFile, limits...)},
		}, nil
	}
	return []Artifact{
		{Kind: ArtifactEntrypoint, InstallPath: ns.RunnerEntrypoint, Mode: 0o700, Contents: renderSimpleLinuxEntrypoint(ns, hostID)},
		{Kind: ArtifactSystemdUnit, InstallPath: filepath.Join("/etc/systemd/system", ns.RunnerUnit), Mode: 0o644, Owner: "root", Group: "root", Contents: renderSimpleSystemdService(ns)},
		{Kind: ArtifactSSHWrapper, InstallPath: filepath.Join(ns.Binaries, "runner-ssh-bridge-forced.sh"), Mode: 0o700, Owner: ns.Account, Group: ns.Account,
			Contents: renderPRDCatalogQueuedSSHWrapper(ns, hostID)},
	}, nil
}

func simpleLaneLogCaptureArgs() []string {
	limits := logcapture.SimpleLaneLimits()
	return []string{
		"--active-file-bytes", fmt.Sprint(limits.ActiveFileBytes),
		"--rotated-file-bytes", fmt.Sprint(limits.RotatedFileBytes),
		"--max-rotated-files", fmt.Sprint(limits.MaxRotatedFiles),
		"--temp-file-bytes", fmt.Sprint(limits.TempFileBytes),
	}
}

func renderPRDCatalogQueuedSSHWrapper(ns config.LaneNamespace, hostID string) string {
	root := shellQuote(ns.ServiceRoot)
	catalogRoot := shellQuote(filepath.Join("/home", ns.Account, "projects-runtime", "runner-releases"))
	selectedHost := shellQuote(hostID)
	controllerMap := shellQuote(filepath.Join(ns.ServiceRoot, "config", "ssh-controller-map.yaml"))
	socket := shellQuote(ns.PrivateSocket)
	return fmt.Sprintf(`#!/bin/sh
set -eu

service_root=%s
catalog_root=%s
selected_host=%s
controller_map=%s
runnerd_socket=%s
expected_command='runner-ssh-bridge --stdio'

fail() {
	printf 'runner SSH bridge: %%s\n' "$1" >&2
	exit 126
}

check_file() {
	path=$1
	mode=$2
	if [ -L "$path" ] || [ ! -f "$path" ] || [ "$(stat -c '%%U:%%a' "$path" 2>/dev/null || true)" != "ubuntu:$mode" ]; then
		fail 'catalog bridge component is not sealed'
	fi
}

if [ "$(id -un)" != ubuntu ] || [ "$#" -ne 1 ]; then
	fail 'authenticated key identity is not permitted'
fi
authenticated_key=$1
case "$authenticated_key" in SHA256:*) ;; *) fail 'authenticated key identity is not permitted' ;; esac
key_fingerprint=${authenticated_key#SHA256:}
case "$key_fingerprint" in ''|*[!A-Za-z0-9+/]*) fail 'authenticated key identity is not permitted' ;; esac
if [ "${#key_fingerprint}" -ne 43 ]; then
	fail 'authenticated key identity is not permitted'
fi
if [ "${SSH_ORIGINAL_COMMAND-}" != "$expected_command" ]; then
	fail 'requested command is not permitted'
fi
if [ -n "${SSH_TTY-}" ]; then
	fail 'PTY is not permitted'
fi
if [ ! -L "$service_root/current" ]; then
	fail 'catalog selector is not installed'
fi
if [ -L "$catalog_root" ] || [ ! -d "$catalog_root" ] || [ "$(stat -c '%%U:%%a' "$catalog_root" 2>/dev/null || true)" != 'ubuntu:700' ]; then
	fail 'catalog root is not private'
fi
release_target=$(readlink "$service_root/current" 2>/dev/null || true)
case "$release_target" in
	"$catalog_root/"*) ;;
	*) fail 'catalog selector target is invalid' ;;
esac
catalog_relative=${release_target#"$catalog_root/"}
release_id=${catalog_relative%%/*}
host_suffix=${catalog_relative#*/}
if [ "$host_suffix" != "$selected_host" ] || [ "$release_target" != "$catalog_root/$release_id/$selected_host" ] || [ "${#release_id}" -gt 64 ]; then
	fail 'catalog selector target is invalid'
fi
case "$release_id" in ''|[!a-z0-9]*|*[!a-z0-9._-]*|dev|qas|prd|latest|current) fail 'catalog release identity is invalid' ;; esac
for path in "$catalog_root/$release_id" "$release_target" "$release_target/bin"; do
	if [ -L "$path" ] || [ ! -d "$path" ] || [ "$(stat -c '%%U:%%a' "$path" 2>/dev/null || true)" != "ubuntu:500" ]; then
		fail 'catalog release directory is not sealed'
	fi
done
check_file "$catalog_root/$release_id/release.json" 400
check_file "$release_target/manifest.json" 400
bridge_bin="$release_target/bin/runner-ssh-bridge"
check_file "$bridge_bin" 500
exec "$bridge_bin" --stdio --authenticated-key "$authenticated_key" --controller-map "$controller_map" --runnerd-socket "$runnerd_socket" --catalog-host "$selected_host" --catalog-lane prd
`, root, catalogRoot, selectedHost, controllerMap, socket)
}
