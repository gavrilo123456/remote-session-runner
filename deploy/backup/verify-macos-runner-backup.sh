#!/bin/sh
set -eu
umask 077

usage() {
	cat >&2 <<'EOF'
usage: verify-macos-runner-backup.sh --archive /absolute/backup.tar.gz [--checksum /absolute/backup.tar.gz.sha256]

Verifies the outer archive checksum and every archive member checksum without
writing to Runner paths or starting a Runner service.
EOF
}

archive=''
checksum=''
while [ "$#" -gt 0 ]; do
	case "$1" in
		--archive)
			[ "$#" -ge 2 ] || { usage; exit 2; }
			archive=$2
			shift 2
			;;
		--checksum)
			[ "$#" -ge 2 ] || { usage; exit 2; }
			checksum=$2
			shift 2
			;;
		--help|-h)
			usage
			exit 0
			;;
		*)
			usage
			exit 2
			;;
	esac
done

case "$archive" in
	/*) ;;
	*) usage; exit 2 ;;
esac
if [ -z "$checksum" ]; then
	checksum="$archive.sha256"
fi
if [ -L "$archive" ] || [ ! -f "$archive" ] || [ -L "$checksum" ] || [ ! -f "$checksum" ]; then
	printf '%s\n' 'archive and checksum must be regular files' >&2
	exit 1
fi

expected=$(/usr/bin/awk 'NR == 1 {print $1; exit}' "$checksum")
case "$expected" in
	????????????????????????????????????????????????????????????????) ;;
	*) printf '%s\n' 'checksum file has an unexpected format' >&2; exit 1 ;;
esac
actual=$(/usr/bin/shasum -a 256 "$archive" | /usr/bin/awk '{print $1}')
if [ "$actual" != "$expected" ]; then
	printf '%s\n' 'archive checksum mismatch' >&2
	exit 1
fi

work=$(mktemp -d "${TMPDIR:-/private/tmp}/remote-session-runner-backup-verify.XXXXXX")
cleanup() {
	status=$?
	trap - EXIT HUP INT TERM
	rm -rf "$work"
	exit "$status"
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

listing="$work/archive-members.txt"
/usr/bin/tar -tzf "$archive" > "$listing"
while IFS= read -r member; do
	case "$member" in
		''|/*|../*|*/../*|..|*'/../'*)
			printf 'archive contains an unsafe member path: %s\n' "$member" >&2
			exit 1
			;;
	esac
done < "$listing"
/usr/bin/tar -xzf "$archive" -C "$work"

if [ -n "$(find "$work" -type l -print -quit)" ]; then
	printf '%s\n' 'archive contains a symlink and is not a supported continuity backup' >&2
	exit 1
fi
if [ -n "$(find "$work" ! -type d ! -type f -print -quit)" ]; then
	printf '%s\n' 'archive contains a non-regular entry and is not a supported continuity backup' >&2
	exit 1
fi
for required in \
	metadata/manifest.txt \
	metadata/SHA256SUMS \
	metadata/service-components.tsv \
	metadata/mailboxes.tsv \
	metadata/external-mailboxes.tsv \
	metadata/ssh.txt \
	payload/service-root/config/mac.yaml \
	payload/service-root/secrets \
	payload/service-root/state \
	payload/service-root/mailbox \
	payload/service-root/mailboxes \
	payload/service-root/backups \
	payload/service-root/toolchains/go1.27.1/bin/go \
	source/remote-session-runner.bundle; do
	if [ ! -e "$work/$required" ] || [ -L "$work/$required" ]; then
		printf 'archive is missing required continuity material: %s\n' "$required" >&2
		exit 1
	fi
done
if ! (cd "$work" && /usr/bin/shasum -a 256 -c metadata/SHA256SUMS >/dev/null); then
	printf '%s\n' 'archive member checksum mismatch' >&2
	exit 1
fi
if ! /usr/bin/grep -Fxq 'format=remote-session-runner-macos-continuity-v1' "$work/metadata/manifest.txt"; then
	printf '%s\n' 'archive format is not a supported Mac continuity backup' >&2
	exit 1
fi
source_revision=$(/usr/bin/sed -n 's/^source_revision=//p' "$work/metadata/manifest.txt")
case "$source_revision" in
	????????????????????????????????????????) ;;
	*) printf '%s\n' 'archive source revision has an unexpected format' >&2; exit 1 ;;
esac
source_origin=$(/usr/bin/sed -n 's/^source_origin=//p' "$work/metadata/manifest.txt")
if [ -z "$source_origin" ]; then
	printf '%s\n' 'archive source origin is unavailable' >&2
	exit 1
fi

printf 'Backup verified: %s\nsource_revision: %s\n' "$archive" "$source_revision"
