package laneinstall

import (
	"encoding/xml"
	"fmt"
	"path/filepath"
	"strings"

	"remote-session-runner/src/internal/config"
)

func renderLaunchAgent(label, root, capture, logPath, program, configPath string, captureLimits ...string) string {
	args := []string{capture, "--log-path", logPath}
	args = append(args, captureLimits...)
	args = append(args, "--", program, "--config", configPath)
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	b.WriteString("<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n")
	b.WriteString("<plist version=\"1.0\">\n<dict>\n")
	writePlistString(&b, "Label", label)
	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	for _, arg := range args {
		b.WriteString("\t\t<string>")
		writeXMLEscaped(&b, arg)
		b.WriteString("</string>\n")
	}
	b.WriteString("\t</array>\n")
	b.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n")
	b.WriteString("\t<key>KeepAlive</key>\n\t<true/>\n")
	b.WriteString("\t<key>ThrottleInterval</key>\n\t<integer>10</integer>\n")
	b.WriteString("\t<key>ExitTimeOut</key>\n\t<integer>15</integer>\n")
	b.WriteString("\t<key>Umask</key>\n\t<integer>63</integer>\n")
	b.WriteString("\t<key>ProcessType</key>\n\t<string>Background</string>\n")
	writePlistString(&b, "WorkingDirectory", root)
	writePlistString(&b, "StandardOutPath", "/dev/null")
	writePlistString(&b, "StandardErrorPath", "/dev/null")
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

func writePlistString(b *strings.Builder, key, value string) {
	fmt.Fprintf(b, "\t<key>%s</key>\n\t<string>", key)
	writeXMLEscaped(b, value)
	b.WriteString("</string>\n")
}

func writeXMLEscaped(b *strings.Builder, value string) {
	if err := xml.EscapeText(b, []byte(value)); err != nil {
		panic("XML string encoding failed for fixed lane template")
	}
}

func renderSystemdService(ns config.LaneNamespace) string {
	logPath := filepath.Join(ns.Logs, "runnerd.log")
	entrypoint := filepath.Join(ns.Binaries, "runnerd-entrypoint.sh")
	capture := strings.Join([]string{ns.LogCapture, "--log-path", logPath, "--", entrypoint}, " ")
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
Slice=%s

[Install]
WantedBy=multi-user.target
`, ns.Lane, ns.Account, ns.Account, ns.ServiceRoot, capture, ns.JournalIdentifier, ns.RunnerSlice)
}

func renderSystemdSlice(ns config.LaneNamespace) string {
	return fmt.Sprintf(`[Unit]
Description=Remote Session Runner %s lane resource slice

[Slice]
`, ns.Lane)
}

func renderLinuxEntrypoint(ns config.LaneNamespace) string {
	root := shellQuote(ns.ServiceRoot)
	account := shellQuote(ns.Account)
	var b strings.Builder
	fmt.Fprintf(&b, `#!/bin/sh
set -eu

service_root=%s
expected_account=%s

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

check_file "$service_root/bin/runnerd" 700
check_file "$service_root/bin/runnerd-entrypoint.sh" 700
check_file "$service_root/bin/runner-log-capture" 700
check_file "$service_root/config/linux.yaml" 600

exec "$service_root/bin/runnerd" --config "$service_root/config/linux.yaml"
`, root, account)
	return b.String()
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
