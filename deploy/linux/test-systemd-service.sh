#!/bin/sh
set -eu

service_root='/home/ubuntu/.local/share/remote-session-runner'
repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd -P)
tmp_root="$service_root/tmp"
invalid_config=''
invalid_output=''
tmp_mode_changed=0
installed=0
expected_build_revision=$(git -C "$repo_root" rev-parse HEAD)

if [ "$(uname -s)" != Linux ] || [ "$(id -un)" != ubuntu ] || [ "$(id -u)" != 1001 ]; then
	printf '%s\n' 'test-systemd-service.sh must run on the selected Linux host as ubuntu (uid 1001)' >&2
	exit 2
fi
if ! sudo -n true 2>/dev/null; then
	printf '%s\n' 'noninteractive sudo is required to install and exercise runnerd.service' >&2
	exit 1
fi

restore_service() {
	if [ -n "$invalid_config" ]; then
		rm -f "$invalid_config" "$invalid_output" 2>/dev/null || true
	fi
	if [ "$tmp_mode_changed" -eq 1 ]; then
		chmod 700 "$tmp_root" 2>/dev/null || true
	fi
	if [ "$installed" -eq 1 ]; then
		sudo -n systemctl reset-failed runnerd.service >/dev/null 2>&1 || true
		sudo -n systemctl start runnerd.service >/dev/null 2>&1 || true
	fi
}
trap restore_service EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

check_active() {
	health_reports_build_revision() {
		health=$(curl --silent --show-error --fail --unix-socket "$service_root/run/runnerd.sock" http://runner/health/ready 2>/dev/null) || return 1
		printf '%s' "$health" |
			/usr/bin/python3 -c 'import json,sys; report=json.load(sys.stdin); sys.exit(0 if report.get("build_revision") == sys.argv[1] else 1)' "$expected_build_revision" >/dev/null 2>&1
	}
	ready=0
	for attempt in $(seq 1 40); do
		if sudo -n systemctl is-active --quiet runnerd.service; then
			main_pid=$(sudo -n systemctl show -p MainPID --value runnerd.service)
			if [ "$main_pid" -gt 1 ] \
				&& [ "$(ps -o user= -p "$main_pid" | tr -d ' ')" = ubuntu ] \
				&& [ "$(stat -c '%u:%a' "$service_root/run/runnerd.sock" 2>/dev/null || true)" = "1001:600" ] \
				&& ss -H -ltn 'sport = :8443' | grep -q '10\.0\.0\.200:8443' \
				&& health_reports_build_revision; then
				ready=1
				break
			fi
		fi
		sleep 0.25
	done
	if [ "$ready" -ne 1 ] || ! sudo -n systemctl is-active --quiet runnerd.service; then
		sudo -n systemctl status --no-pager runnerd.service >&2 || true
		printf '%s\n' 'runnerd did not become ready with its private socket, HTTPS listener, and expected build revision' >&2
		return 1
	fi
	main_pid=$(sudo -n systemctl show -p MainPID --value runnerd.service)
	if [ "$main_pid" -le 1 ] || [ "$(ps -o user= -p "$main_pid" | tr -d ' ')" != ubuntu ]; then
		printf 'runnerd MainPID is not running as ubuntu: %s\n' "$main_pid" >&2
		return 1
	fi
	if [ "$(sudo -n systemctl show -p User --value runnerd.service)" != ubuntu ]; then
		printf '%s\n' 'runnerd unit User property is not ubuntu' >&2
		return 1
	fi
	if [ "$(stat -c '%u:%a' "$service_root/run/runnerd.sock")" != "1001:600" ]; then
		printf '%s\n' 'runnerd private socket is not owned by ubuntu with mode 0600' >&2
		return 1
	fi
	if ! ss -H -ltn 'sport = :8443' | grep -q '10\.0\.0\.200:8443'; then
		printf '%s\n' 'runnerd direct HTTPS listener is not bound to 10.0.0.200:8443' >&2
		return 1
	fi
}

check_stopped() {
	for attempt in $(seq 1 40); do
		if ! sudo -n systemctl is-active --quiet runnerd.service; then
			break
		fi
		sleep 0.25
	done
	if sudo -n systemctl is-active --quiet runnerd.service; then
		printf '%s\n' 'runnerd.service remained active after stop' >&2
		return 1
	fi
	if [ -e "$service_root/run/runnerd.sock" ]; then
		printf '%s\n' 'runnerd private socket remains after service stop' >&2
		return 1
	fi
	if ss -H -ltn 'sport = :8443' | grep -q '10\.0\.0\.200:8443'; then
		printf '%s\n' 'runnerd direct HTTPS listener remains after service stop' >&2
		return 1
	fi
}

check_no_installer_cache() {
	leftover_cache=$(find "$tmp_root" -mindepth 1 -maxdepth 1 \
		-name 'install-go-cache.*' \( -type d -o -type l \) -print -quit)
	if [ -n "$leftover_cache" ]; then
		printf 'runnerd installer left a private Go cache: %s\n' "$leftover_cache" >&2
		return 1
	fi
}

check_insecure_path_rejected() {
	rejected=0
	for attempt in $(seq 1 40); do
		active_state=$(sudo -n systemctl show -p ActiveState --value runnerd.service)
		if [ "$active_state" = failed ]; then
			result=$(sudo -n systemctl show -p Result --value runnerd.service)
			exit_status=$(sudo -n systemctl show -p ExecMainStatus --value runnerd.service)
			if [ "$result" = exit-code ] && [ "$exit_status" = 78 ]; then
				rejected=1
				break
			fi
			printf 'runnerd failed for an unexpected reason with Result=%s ExecMainStatus=%s\n' "$result" "$exit_status" >&2
			sudo -n systemctl status --no-pager runnerd.service >&2 || true
			return 1
		fi
		sleep 0.25
	done
	if [ "$rejected" -ne 1 ]; then
		printf 'runnerd did not reject the insecure service path; ActiveState=%s\n' \
			"$(sudo -n systemctl show -p ActiveState --value runnerd.service)" >&2
		sudo -n systemctl status --no-pager runnerd.service >&2 || true
		return 1
	fi
	if [ -e "$service_root/run/runnerd.sock" ]; then
		printf '%s\n' 'runnerd created its socket despite an invalid owner-only service path' >&2
		return 1
	fi
	if ss -H -ltn 'sport = :8443' | grep -q '10\.0\.0\.200:8443'; then
		printf '%s\n' 'runnerd opened its HTTPS listener despite an invalid owner-only service path' >&2
		return 1
	fi
}

installed=1
(cd "$repo_root" && deploy/linux/install-systemd-service.sh)
check_active
check_no_installer_cache

invalid_config="$service_root/config/.p126-invalid-$$.yaml"
invalid_output="$service_root/tmp/.p126-invalid-$$.log"
sed 's/runtime_adapter: linux-host-process/runtime_adapter: invalid-host-profile/' \
	"$service_root/config/linux.yaml" > "$invalid_config"
chmod 600 "$invalid_config"
if "$service_root/bin/runnerd" --config "$invalid_config" >"$invalid_output" 2>&1; then
	rm -f "$invalid_config" "$invalid_output"
	printf '%s\n' 'runnerd accepted an invalid host-process profile' >&2
	exit 1
fi
if ! grep -q 'runnerd: load config:' "$invalid_output"; then
	rm -f "$invalid_config" "$invalid_output"
	printf '%s\n' 'invalid host-process profile did not fail at configuration validation' >&2
	exit 1
fi
rm -f "$invalid_config" "$invalid_output"

sudo -n systemctl stop runnerd.service
check_stopped
sudo -n systemctl start runnerd.service
check_active
sudo -n systemctl restart runnerd.service
check_active

sudo -n systemctl stop runnerd.service
check_stopped
chmod 755 "$tmp_root"
tmp_mode_changed=1
sudo -n systemctl start runnerd.service || true
check_insecure_path_rejected
chmod 700 "$tmp_root"
tmp_mode_changed=0
sudo -n systemctl reset-failed runnerd.service
sudo -n systemctl start runnerd.service
check_active

printf 'P126 PASS: systemd start/stop/restart as ubuntu, owner-only socket, mTLS listener lifecycle, source-revision readiness attestation, invalid profile rejection, and insecure service-path readiness rejection\n'
