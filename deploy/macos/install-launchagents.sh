#!/bin/sh
set -eu

if [ "$(uname -s)" != Darwin ] || [ "$(id -un)" != tomasz.walczuk ]; then
	printf '%s\n' 'install-launchagents.sh must run on the selected Mac account tomasz.walczuk' >&2
	exit 2
fi

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd -P)
service_root='/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner'
config_file="$service_root/config/mac.yaml"
launch_agents="$HOME/Library/LaunchAgents"
go_bin="$service_root/toolchains/go1.27.1/bin/go"
uid=$(id -u)

if [ ! -x "$go_bin" ]; then
	printf 'Go 1.27.1 toolchain missing: %s\n' "$go_bin" >&2
	exit 1
fi

ensure_private_directory() {
	directory=$1
	if [ -L "$directory" ]; then
		printf 'refusing symlinked service directory: %s\n' "$directory" >&2
		exit 1
	fi
	mkdir -p "$directory"
	if [ -L "$directory" ] || [ ! -d "$directory" ] || [ "$(stat -f '%u' "$directory")" != "$uid" ]; then
		printf 'service directory must be a real directory owned by uid %s: %s\n' "$uid" "$directory" >&2
		exit 1
	fi
	chmod 700 "$directory"
}

for directory in \
	"$service_root" "$service_root/bin" "$service_root/config" "$service_root/logs" \
	"$service_root/run" "$service_root/state" "$service_root/mailbox" \
	"$service_root/mailbox/inbox" "$service_root/mailbox/outbox" \
	"$service_root/mailbox/events" "$service_root/mailbox/acks" \
	"$service_root/workspaces" "$service_root/tmp" "$service_root/tmp/scripts" \
	"$service_root/backups" "$service_root/secrets" "$launch_agents"; do
	if [ "$directory" = "$launch_agents" ]; then
		mkdir -p "$directory"
		chmod 755 "$directory"
	else
		ensure_private_directory "$directory"
	fi
done

if [ -L "$config_file" ]; then
	printf 'refusing symlinked Mac config: %s\n' "$config_file" >&2
	exit 1
fi
if [ ! -f "$config_file" ]; then
	install -m 600 "$repo_root/deploy/macos/mac.yaml.example" "$config_file"
	printf 'Created selected Mac config for review: %s\nRerun this installer after reviewing it.\n' "$config_file"
	exit 0
fi
if [ "$(stat -f '%Lp' "$config_file")" != 600 ] || [ "$(stat -f '%u' "$config_file")" != "$uid" ]; then
	printf 'Mac config must be owned by this account with mode 0600: %s\n' "$config_file" >&2
	exit 1
fi

for name in runner runner-local runner-locald; do
	temporary="$service_root/bin/.${name}.$$"
	trap 'rm -f "$temporary"' EXIT HUP INT TERM
	(cd "$repo_root" && GOTOOLCHAIN=local "$go_bin" build -o "$temporary" "./src/cmd/$name")
	chmod 700 "$temporary"
	mv -f "$temporary" "$service_root/bin/$name"
	trap - EXIT HUP INT TERM
done

for name in com.remote-session-runner.locald com.remote-session-runner.local; do
	plist="$repo_root/deploy/macos/launchagents/$name.plist"
	installed="$launch_agents/$name.plist"
	/usr/bin/plutil -lint "$plist" >/dev/null
	launchctl bootout "gui/$uid" "$installed" >/dev/null 2>&1 || true
	install -m 600 "$plist" "$installed"
	launchctl bootstrap "gui/$uid" "$installed"
	launchctl kickstart -k "gui/$uid/$name"
done
