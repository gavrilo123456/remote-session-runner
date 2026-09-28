#!/bin/sh
set -eu
umask 077

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd -P)
service_root='/home/ubuntu/.local/share/remote-session-runner'
config_file="$service_root/config/linux.yaml"
socket_path="$service_root/run/runnerd.sock"
go_bin=${GO:-"$service_root/toolchains/go1.27.1/bin/go"}
expected_commit=${RSR_P129_EXPECTED_COMMIT:-${RSR_P128_EXPECTED_COMMIT:-}}
: "${expected_commit:?set to the exact P129 commit already pushed and pulled to Ubuntu}"
stopped=0
scratch=''
backup_ready=0
p128_ready=0
preserve_scratch=0
restore_blocked=0
database_path="$service_root/state/remote.db"

if [ "$(uname -s)" != Linux ] || [ "$(id -un)" != ubuntu ] || [ "$(id -u)" != 1001 ]; then
	printf '%s\n' 'test-ops-health.sh must run on the selected Ubuntu account (uid 1001)' >&2
	exit 2
fi
if [ "$(git -C "$repo_root" branch --show-current)" != dev ] \
	|| [ "$(git -C "$repo_root" rev-parse HEAD)" != "$expected_commit" ] \
	|| [ "$(git -C "$repo_root" rev-parse origin/dev)" != "$expected_commit" ] \
	|| [ -n "$(git -C "$repo_root" status --porcelain)" ]; then
	printf '%s\n' 'Ubuntu checkout is not clean dev at the expected synchronized P128 commit' >&2
	exit 1
fi
if ! sudo -n true 2>/dev/null; then
	printf '%s\n' 'noninteractive sudo is required for the P128 runnerd restart' >&2
	exit 1
fi

restore_service() {
	status=$?
	trap - EXIT HUP INT TERM
	if [ "$stopped" -eq 1 ] && [ "$backup_ready" -eq 1 ] && [ "$p128_ready" -eq 0 ]; then
		if rollback_pre_health_failure; then
			stopped=0
			backup_ready=0
		else
			printf 'P128 rollback did not finish safely; retain the temporary recovery files at %s\n' "$scratch" >&2
			preserve_scratch=1
			restore_blocked=1
			status=1
		fi
	fi
	if [ "$stopped" -eq 1 ] && [ "$restore_blocked" -eq 0 ] && ! sudo -n systemctl is-active --quiet runnerd.service; then
		if sudo -n systemctl start runnerd.service >/dev/null 2>&1; then
			printf '%s\n' 'restored runnerd.service to active state after P128 host-gate interruption' >&2
		else
			printf '%s\n' 'could not restore runnerd.service; inspect systemd before proceeding' >&2
			status=1
		fi
	fi
	if [ -n "$scratch" ] && [ "$preserve_scratch" -eq 0 ]; then
		rm -rf "$scratch"
	fi
	exit "$status"
}

rollback_pre_health_failure() {
	sudo -n systemctl stop runnerd.service >/dev/null 2>&1 || true
	for attempt in $(seq 1 40); do
		if ! sudo -n systemctl is-active --quiet runnerd.service \
			&& [ ! -e "$socket_path" ] \
			&& ! ss -H -ltn 'sport = :8443' | grep -q '10\.0\.0\.200:8443'; then
			break
		fi
		sleep 0.25
	done
	if sudo -n systemctl is-active --quiet runnerd.service || [ -e "$socket_path" ] || ss -H -ltn 'sport = :8443' | grep -q '10\.0\.0\.200:8443'; then
		printf '%s\n' 'refusing P128 rollback while runnerd still owns its socket or public listener' >&2
		return 1
	fi
	install -o ubuntu -g ubuntu -m 700 "$scratch/runnerd.before" "$service_root/bin/runnerd" || return 1
	cp -p "$scratch/remote.db.before" "$database_path" || return 1
	for suffix in -wal -shm; do
		if [ -f "$scratch/remote.db${suffix}.present" ]; then
			cp -p "$scratch/remote.db${suffix}.before" "$database_path$suffix" || return 1
		else
			rm -f "$database_path$suffix" || return 1
		fi
	done
	sudo -n systemctl start runnerd.service >/dev/null 2>&1 || return 1
	for attempt in $(seq 1 40); do
		if sudo -n systemctl is-active --quiet runnerd.service \
			&& [ -S "$socket_path" ] \
			&& ss -H -ltn 'sport = :8443' | grep -q '10\.0\.0\.200:8443'; then
			printf '%s\n' 'P128 pre-readiness rollback restored the prior runnerd binary and SQLite files.' >&2
			return 0
		fi
		sleep 0.25
	done
	printf '%s\n' 'restored P127 service did not return active after P128 rollback' >&2
	return 1
}
trap restore_service EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

if [ ! -x "$go_bin" ] || [ ! -f "$config_file" ]; then
	printf '%s\n' 'selected Go toolchain or owner-only Linux config is unavailable' >&2
	exit 1
fi

make -C "$repo_root" test
make -C "$repo_root" vet
make -C "$repo_root" build
make -C "$repo_root" smoke
GOTOOLCHAIN=local "$go_bin" test -race \
	./src/internal/config ./src/internal/opshealth ./src/internal/store \
	./src/internal/dispatcher ./src/internal/localapi ./src/internal/runnerlocal \
	./src/internal/runnerlocald ./src/internal/runnerd
GOTOOLCHAIN=local "$go_bin" test -tags=p128opshost -run '^$' \
	./src/internal/runnerlocal ./src/internal/runnerlocald
GOTOOLCHAIN=local "$go_bin" test -tags=p128hoststatus -run '^$' ./src/internal/store

scratch=$(mktemp -d "$service_root/tmp/p128-ops.XXXXXX")
chmod 700 "$scratch"
new_runnerd="$scratch/runnerd"
GOTOOLCHAIN=local "$go_bin" build -o "$new_runnerd" ./src/cmd/runnerd

invalid_profile="$scratch/invalid-profile.yaml"
invalid_profile_output="$scratch/invalid-profile.json"
sed 's/runtime_adapter: linux-host-process/runtime_adapter: invalid-p128-host-profile/' "$config_file" > "$invalid_profile"
chmod 600 "$invalid_profile"
if "$new_runnerd" doctor --config "$invalid_profile" > "$invalid_profile_output" 2>&1; then
	printf '%s\n' 'P128 runnerd doctor accepted an invalid Linux host profile' >&2
	exit 1
fi
grep -Fq '"component": "linux_host_profile"' "$invalid_profile_output"
grep -Fq '"reason": "host_profile_not_ready"' "$invalid_profile_output"
if grep -Eq '/secrets/|PRIVATE KEY|server\.key' "$invalid_profile_output"; then
	printf '%s\n' 'invalid-profile doctor report exposed credential information' >&2
	exit 1
fi

missing_mtls="$scratch/missing-mtls.yaml"
missing_mtls_output="$scratch/missing-mtls.json"
sed '/^secret_references:/,/^environment_registry:/ { /^environment_registry:/!d; }' "$config_file" > "$missing_mtls"
chmod 600 "$missing_mtls"
if "$new_runnerd" doctor --config "$missing_mtls" > "$missing_mtls_output" 2>&1; then
	printf '%s\n' 'P128 runnerd doctor accepted configuration without its mandatory mTLS key reference' >&2
	exit 1
fi
grep -Fq '"component": "direct_mtls"' "$missing_mtls_output"
grep -Fq '"reason": "mtls_configuration_not_ready"' "$missing_mtls_output"
if grep -Eq '/secrets/|PRIVATE KEY|server\.key' "$missing_mtls_output"; then
	printf '%s\n' 'missing-mTLS doctor report exposed credential information' >&2
	exit 1
fi
printf '%s\n' 'P128 Ubuntu doctor negatives passed: invalid host profile and absent mandatory mTLS reference; reports contain no credential details.'

make -C "$repo_root" test-p128-host-status
sudo -n systemctl stop runnerd.service
stopped=1
if sudo -n systemctl is-active --quiet runnerd.service || [ -e "$socket_path" ] || ss -H -ltn 'sport = :8443' | grep -q '10\.0\.0\.200:8443'; then
	printf '%s\n' 'runnerd did not stop cleanly before the P128 migration doctor' >&2
	exit 1
fi
make -C "$repo_root" test-p128-host-status

cp -p "$service_root/bin/runnerd" "$scratch/runnerd.before"
cp -p "$database_path" "$scratch/remote.db.before"
for suffix in -wal -shm; do
	if [ -L "$database_path$suffix" ]; then
		printf 'refusing symlinked SQLite sidecar during P128 recovery snapshot: %s\n' "$database_path$suffix" >&2
		exit 1
	fi
	if [ -f "$database_path$suffix" ]; then
		cp -p "$database_path$suffix" "$scratch/remote.db${suffix}.before"
		: > "$scratch/remote.db${suffix}.present"
	fi
done
backup_ready=1
healthy_doctor="$scratch/healthy-doctor.json"
if ! "$new_runnerd" doctor --config "$config_file" > "$healthy_doctor" 2>&1; then
	cat "$healthy_doctor" >&2
	printf '%s\n' 'P128 runnerd doctor failed against the selected Ubuntu profile' >&2
	exit 1
fi
grep -Fq '"component": "linux_runnerd"' "$healthy_doctor"
grep -Fq '"readiness": "ready"' "$healthy_doctor"
grep -Fq '"active_session_slots"' "$healthy_doctor"
grep -Fq '"cleanup_failures_total"' "$healthy_doctor"
if grep -Eq '/secrets/|PRIVATE KEY|server\.key' "$healthy_doctor"; then
	printf '%s\n' 'healthy doctor report exposed credential information' >&2
	exit 1
fi
cat "$healthy_doctor"

(cd "$repo_root" && deploy/linux/install-systemd-service.sh)
ready=0
for attempt in $(seq 1 40); do
	if sudo -n systemctl is-active --quiet runnerd.service \
		&& [ -S "$socket_path" ] \
		&& [ "$(stat -c '%u:%a' "$socket_path")" = '1001:600' ] \
		&& ss -H -ltn 'sport = :8443' | grep -q '10\.0\.0\.200:8443'; then
		ready=1
		break
	fi
	sleep 0.25
done
if [ "$ready" -ne 1 ]; then
	printf '%s\n' 'P128 runnerd did not return active with its private socket and direct HTTPS listener' >&2
	exit 1
fi
p128_ready=1
stopped=0

live="$scratch/health-live.json"
ready_report="$scratch/health-ready.json"
metrics_report="$scratch/metrics.json"
curl --silent --show-error --fail --unix-socket "$socket_path" http://runner/health/live > "$live"
curl --silent --show-error --fail --unix-socket "$socket_path" http://runner/health/ready > "$ready_report"
curl --silent --show-error --fail --unix-socket "$socket_path" http://runner/metrics > "$metrics_report"
grep -Fq '"liveness":"live"' "$live"
grep -Fq '"component":"linux_runnerd"' "$ready_report"
grep -Fq '"readiness":"ready"' "$ready_report"
grep -Fq '"active_session_slots"' "$metrics_report"
grep -Fq '"mailbox_backlog"' "$metrics_report"
cat "$ready_report"
make -C "$repo_root" test-p128-host-readonly-health

printf '%s\n' 'P128/P129 Ubuntu host gate passed: safe doctor negatives, healthy doctor and bounded metrics, schema migration, active Linux profile, owner-only private health socket, readiness and metrics routes, and read-only SQLite write-failure detection.'
