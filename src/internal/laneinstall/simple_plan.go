package laneinstall

import (
	"fmt"
	"path/filepath"
	"strings"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/logcapture"
)

// BuildSimplePlan derives service artifacts for the active existing-host
// profile. The older BuildPlan remains available to historical advanced-track
// tests; SIMPLE installers must use this function so they cannot select a
// second SSH daemon, alternate SSH port, cgroup slice, or release authority.
func BuildSimplePlan(hostID string, lane config.ReleaseLane) (Plan, error) {
	if lane != config.ReleaseLaneDev && lane != config.ReleaseLaneQAS {
		return Plan{}, ErrInvalidLane
	}
	tuple, ok := canonicalHosts[hostID]
	if !ok {
		return Plan{}, ErrInvalidHost
	}
	ns, err := config.DeriveSimpleLaneNamespace(tuple.kind, lane, tuple.profile)
	if err != nil {
		return Plan{}, fmt.Errorf("derive fixed simple namespace: %w", err)
	}
	plan := Plan{HostID: hostID, Lane: lane, Namespace: ns}
	if tuple.kind == config.HostKindMac {
		plan.Artifacts = simpleMacArtifacts(ns)
	} else {
		plan.Artifacts = simpleLinuxArtifacts(ns, hostID)
	}
	return plan, nil
}

func simpleMacArtifacts(ns config.LaneNamespace) []Artifact {
	current := filepath.Join(ns.ServiceRoot, "current", "bin")
	limits := logcapture.SimpleLaneLimits()
	limitArgs := []string{"--active-file-bytes", fmt.Sprint(limits.ActiveFileBytes), "--rotated-file-bytes", fmt.Sprint(limits.RotatedFileBytes), "--max-rotated-files", fmt.Sprint(limits.MaxRotatedFiles), "--temp-file-bytes", fmt.Sprint(limits.TempFileBytes)}
	return []Artifact{
		{Kind: ArtifactLaunchAgent, InstallPath: ns.LocalAgentPlist, Mode: 0o644, Contents: renderLaunchAgent(ns.LocalAgentLabel, ns.ServiceRoot,
			filepath.Join(current, "runner-log-capture"), filepath.Join(ns.Logs, "local.log"), filepath.Join(current, "runner-local"), ns.ConfigFile, limitArgs...)},
		{Kind: ArtifactLaunchAgent, InstallPath: ns.LocalDAgentPlist, Mode: 0o644, Contents: renderLaunchAgent(ns.LocalDAgentLabel, ns.ServiceRoot,
			filepath.Join(current, "runner-log-capture"), filepath.Join(ns.Logs, "locald.log"), filepath.Join(current, "runner-locald"), ns.ConfigFile, limitArgs...)},
	}
}

func simpleLinuxArtifacts(ns config.LaneNamespace, hostID string) []Artifact {
	return []Artifact{
		{Kind: ArtifactEntrypoint, InstallPath: ns.RunnerEntrypoint, Mode: 0o700, Contents: renderSimpleLinuxEntrypoint(ns, hostID)},
		{Kind: ArtifactSystemdUnit, InstallPath: filepath.Join("/etc/systemd/system", ns.RunnerUnit), Mode: 0o644, Owner: "root", Group: "root", Contents: renderSimpleSystemdService(ns)},
	}
}

func renderSimpleSystemdService(ns config.LaneNamespace) string {
	logPath := filepath.Join(ns.Logs, "runnerd.log")
	currentBin := filepath.Join(ns.ServiceRoot, "current", "bin")
	limits := logcapture.SimpleLaneLimits()
	// systemd parses ExecStart directly; these fixed Linux paths contain no
	// whitespace and must not receive shell-style single quotes.
	capture := fmt.Sprintf("%s --log-path %s --active-file-bytes %d --rotated-file-bytes %d --max-rotated-files %d --temp-file-bytes %d -- %s",
		filepath.Join(currentBin, "runner-log-capture"), logPath, limits.ActiveFileBytes, limits.RotatedFileBytes, limits.MaxRotatedFiles, limits.TempFileBytes, ns.RunnerEntrypoint)
	return fmt.Sprintf(`[Unit]
Description=Remote Session Runner %s lane execution service
Wants=network-online.target
After=network-online.target
StartLimitIntervalSec=60s
StartLimitBurst=3

[Service]
Type=simple
User=%s
Group=%s
WorkingDirectory=%s
Environment=HOME=/home/ubuntu
UMask=0077
ExecStart=%s
Restart=on-failure
RestartSec=3s
RestartPreventExitStatus=78
KillMode=mixed
TimeoutStopSec=30s
StandardOutput=null
StandardError=null
SyslogIdentifier=%s

[Install]
WantedBy=multi-user.target
`, ns.Lane, ns.Account, ns.Account, ns.ServiceRoot, capture, ns.JournalIdentifier)
}

func renderSimpleLinuxEntrypoint(ns config.LaneNamespace, hostID string) string {
	root := shellQuote(ns.ServiceRoot)
	account := shellQuote(ns.Account)
	catalogRoot := shellQuote(filepath.Join("/home", ns.Account, "projects-runtime", "runner-releases"))
	selectedHost := shellQuote(hostID)
	var b strings.Builder
	fmt.Fprintf(&b, `#!/bin/sh
set -eu

service_root=%s
expected_account=%s
catalog_root=%s
selected_host=%s

fail_path_check() {
	printf 'runnerd: lane service path is not ready: %%s\n' "$1" >&2
	exit 78
}

check_real_directory() {
	path=$1
	if [ -L "$path" ] || [ ! -d "$path" ]; then
		fail_path_check "$path must be a real directory"
	fi
}

check_directory() {
	path=$1
	check_real_directory "$path"
	if [ "$(stat -c '%%U:%%a' "$path" 2>/dev/null || true)" != "$expected_account:700" ]; then
		fail_path_check "$path must be owned by $expected_account with mode 0700"
	fi
}

check_file() {
	path=$1
	mode=$2
	if [ -L "$path" ] || [ ! -f "$path" ]; then
		fail_path_check "$path must be a regular file"
	fi
	if [ "$(stat -c '%%U:%%a' "$path" 2>/dev/null || true)" != "$expected_account:$mode" ]; then
		fail_path_check "$path must be owned by $expected_account with mode $mode"
	fi
}

check_sealed_directory() {
	path=$1
	if [ -L "$path" ] || [ ! -d "$path" ]; then
		fail_path_check "$path must be a real sealed release directory"
	fi
	if [ "$(stat -c '%%U:%%a' "$path" 2>/dev/null || true)" != "$expected_account:500" ]; then
		fail_path_check "$path must be owned by $expected_account with mode 0500"
	fi
}

if [ "$(id -un)" != "$expected_account" ]; then
	fail_path_check "service account must be $expected_account"
fi

for path in /home /home/ubuntu /home/ubuntu/.local /home/ubuntu/.local/share; do
	check_real_directory "$path"
done

for path in \
	"$service_root" "$service_root/bin" "$service_root/config" \
	"$service_root/run" "$service_root/state" "$service_root/tmp" \
	"$service_root/tmp/scripts" "$service_root/workspaces" \
	"$service_root/backups" "$service_root/logs" "$service_root/staging" \
	"$service_root/secrets"; do
	check_directory "$path"
done

if [ ! -L "$service_root/current" ] || [ ! -d "$service_root/current" ]; then
	fail_path_check "$service_root/current must resolve to a selected release directory"
fi
release_target=$(readlink "$service_root/current" 2>/dev/null || true)
catalog_selected=0
case "$release_target" in
	releases/*)
		release_id=${release_target#releases/}
		if [ "${#release_id}" -ne 64 ]; then
			fail_path_check "$service_root/current legacy release identity is invalid"
		fi
		case "$release_id" in *[!0-9a-f]*) fail_path_check "$service_root/current legacy release identity is invalid" ;; esac
		check_directory "$service_root/releases"
		bundle_root="$service_root/$release_target"
		check_directory "$bundle_root"
		;;
	"$catalog_root/"*)
		catalog_selected=1
		catalog_relative=${release_target#"$catalog_root/"}
		release_id=${catalog_relative%%/*}
		host_suffix=${catalog_relative#*/}
		if [ "$host_suffix" != "$selected_host" ] || [ "$release_target" != "$catalog_root/$release_id/$selected_host" ]; then
			fail_path_check "$service_root/current catalog selector must target this host's canonical release directory"
		fi
		case "$release_id" in
			''|[!a-z0-9]*|*[!a-z0-9._-]*|dev|qas|prd|latest|current)
				fail_path_check "$service_root/current catalog release identity is invalid" ;;
		esac
		if [ "${#release_id}" -gt 64 ]; then
			fail_path_check "$service_root/current catalog release identity is invalid"
		fi
		bundle_root=$release_target
		check_real_directory "$(dirname "$catalog_root")"
		check_directory "$catalog_root"
		check_sealed_directory "$catalog_root/$release_id"
		check_file "$catalog_root/$release_id/release.json" 400
		check_file "$bundle_root/manifest.json" 400
		;;
	*) fail_path_check "$service_root/current must target a versioned legacy or canonical catalog release" ;;
esac
if [ "$catalog_selected" -eq 1 ]; then
	check_sealed_directory "$bundle_root"
	check_sealed_directory "$bundle_root/bin"
	check_file "$service_root/current/bin/runnerd" 500
	check_file "$service_root/current/bin/runner-log-capture" 500
else
	check_real_directory "$bundle_root"
	check_file "$service_root/current/bin/runnerd" 700
	check_file "$service_root/current/bin/runner-log-capture" 700
fi
check_file "$service_root/bin/runnerd-entrypoint.sh" 700
check_file "$service_root/config/linux.yaml" 600

exec "$service_root/current/bin/runnerd" --config "$service_root/config/linux.yaml"
`, root, account, catalogRoot, selectedHost)
	return b.String()
}
