package laneinstall

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/sshdeploy"
)

func TestLanesL202BuildsExactPerLaneQueuedSSHPlan(t *testing.T) {
	profiles := map[string]struct {
		public string
		bind   string
	}{
		"linux-host":      {public: "129.151.232.40", bind: "10.0.0.200"},
		"sandbox-host":    {public: "132.226.205.205", bind: "10.0.0.14"},
		"factory-sandbox": {public: "145.241.255.66", bind: "10.0.0.30"},
	}
	for host, profile := range profiles {
		for _, lane := range []config.ReleaseLane{config.ReleaseLaneDev, config.ReleaseLaneQAS} {
			dispatcher := l202PublicKey("dispatcher-" + host + "-" + string(lane))
			server := l202PublicKey("host-" + host + "-" + string(lane))
			plan, err := BuildQueuedSSHPlan(host, lane, dispatcher, server)
			if err != nil {
				t.Fatalf("BuildQueuedSSHPlan(%s, %s): %v", host, lane, err)
			}
			wantPort := 2224
			if lane == config.ReleaseLaneQAS {
				wantPort = 2225
			}
			if plan.HostID != host || plan.Lane != lane || plan.PublicHost != profile.public || plan.Bind != fmt.Sprintf("%s:%d", profile.bind, wantPort) || plan.Port != wantPort {
				t.Fatalf("%s/%s listener tuple = %+v", host, lane, plan)
			}
			if plan.Namespace.QueuedSSHUnit != "runner-sshd-"+string(lane)+".service" || plan.Namespace.RunnerSlice != "remote-session-runner-"+string(lane)+".slice" {
				t.Fatalf("%s/%s unit/slice = %+v", host, lane, plan.Namespace)
			}
			if plan.HostKeyPath != filepath.Join("/etc/remote-session-runner/ssh", string(lane), "ssh_host_ed25519_key") ||
				plan.AuthorizedKeysPath != filepath.Join("/etc/remote-session-runner/ssh", string(lane), "authorized_keys") ||
				plan.SSHDConfigPath != filepath.Join("/etc/remote-session-runner/ssh", string(lane), "sshd_config") ||
				plan.ForcedWrapperPath != filepath.Join("/etc/remote-session-runner/ssh", string(lane), "runner-ssh-bridge-forced.sh") {
				t.Fatalf("%s/%s authority paths = %+v", host, lane, plan)
			}
			if !strings.HasSuffix(plan.ClientKnownHostsPath, filepath.Join("profiles", host, "ssh", "known_hosts")) ||
				!strings.HasSuffix(plan.ClientDispatcherPrivateKey, filepath.Join("profiles", host, "ssh", "dispatcher_ed25519")) ||
				strings.Contains(plan.ClientDispatcherPrivateKey, "RemoteSessionRunner/") {
				t.Fatalf("%s/%s client identity paths = %+v", host, lane, plan)
			}
			if len(plan.Artifacts) != 5 {
				t.Fatalf("%s/%s emitted %d artifacts, want config, authorized key, wrapper, unit and known_hosts", host, lane, len(plan.Artifacts))
			}
			for _, artifact := range plan.Artifacts {
				if artifact.InstallPath == "" || artifact.Contents == "" || artifact.Owner == "" || artifact.Group == "" {
					t.Fatalf("%s/%s has incomplete artifact metadata: %+v", host, lane, artifact)
				}
				if strings.Contains(artifact.InstallPath, "/etc/ssh/sshd_config") || strings.Contains(artifact.InstallPath, "/home/ubuntu/.ssh/authorized_keys") || strings.Contains(artifact.Contents, "Port 22\n") {
					t.Fatalf("%s/%s plan targets PRD/global SSH authority: %+v", host, lane, artifact)
				}
			}
		}
	}
}

func TestLanesL202RejectsUnsupportedHostsLanesAndKeys(t *testing.T) {
	validDispatcher := l202PublicKey("dispatcher")
	validHost := l202PublicKey("host")
	for _, host := range []string{"", "mac-control-plane", "ubuntu", "unknown-host"} {
		if _, err := BuildQueuedSSHPlan(host, config.ReleaseLaneDev, validDispatcher, validHost); err != ErrInvalidHost {
			t.Errorf("BuildQueuedSSHPlan(%q) error = %v, want %v", host, err, ErrInvalidHost)
		}
	}
	for _, lane := range []config.ReleaseLane{config.ReleaseLanePRD, "", "test"} {
		if _, err := BuildQueuedSSHPlan("linux-host", lane, validDispatcher, validHost); err != ErrInvalidLane {
			t.Errorf("BuildQueuedSSHPlan(%q) error = %v, want %v", lane, err, ErrInvalidLane)
		}
	}
	for _, key := range []string{"", "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQ==", validDispatcher + "\n" + validDispatcher, "ssh-ed25519 !!!"} {
		if _, err := BuildQueuedSSHPlan("linux-host", config.ReleaseLaneDev, key, validHost); err == nil {
			t.Errorf("malformed dispatcher key %q was accepted", key)
		}
		if _, err := BuildQueuedSSHPlan("linux-host", config.ReleaseLaneDev, validDispatcher, key); err == nil {
			t.Errorf("malformed host key %q was accepted", key)
		}
	}
	for _, newline := range []string{"\n", "\r\n"} {
		if _, err := BuildQueuedSSHPlan("linux-host", config.ReleaseLaneDev, validDispatcher+newline, validHost+newline); err != nil {
			t.Errorf("one terminal public-key line ending %q was rejected: %v", newline, err)
		}
	}
	if _, err := BuildQueuedSSHPlan("linux-host", config.ReleaseLaneDev, validDispatcher+"\n\n", validHost); err == nil {
		t.Fatal("multiple dispatcher public-key lines were accepted")
	}
	badBlob := base64.StdEncoding.EncodeToString([]byte("not-an-ssh-key"))
	if _, err := BuildQueuedSSHPlan("linux-host", config.ReleaseLaneDev, "ssh-ed25519 "+badBlob, validHost); err == nil {
		t.Fatal("Ed25519 label with a malformed SSH key blob was accepted")
	}
}

func TestLanesL202SSHDConfigClosesInteractiveAndForwardingAccess(t *testing.T) {
	plan := l202TestPlan(t, "linux-host", config.ReleaseLaneDev)
	configArtifact := l202Artifact(t, plan, ArtifactSSHConfig)
	contents := configArtifact.Contents
	for _, required := range []string{
		"AddressFamily inet", "Port 2224", "ListenAddress 10.0.0.200:2224", "HostKey " + plan.HostKeyPath,
		"UsePAM no", "PermitRootLogin no", "AllowUsers ubuntu", "AuthenticationMethods publickey",
		"PubkeyAuthentication yes", "PasswordAuthentication no", "KbdInteractiveAuthentication no",
		"AuthorizedKeysFile " + plan.AuthorizedKeysPath, "PermitTTY no", "AllowAgentForwarding no",
		"AllowTcpForwarding no", "AllowStreamLocalForwarding no", "X11Forwarding no", "PermitTunnel no",
		"PermitUserEnvironment no", "PermitUserRC no", "MaxSessions 1", "LoginGraceTime 30s",
		"MaxAuthTries 3", "MaxStartups 10:30:30", "ForceCommand " + plan.ForcedWrapperPath,
	} {
		if !strings.Contains(contents, required) {
			t.Errorf("sshd config missing %q", required)
		}
	}
	if strings.Contains(contents, "\nInclude ") || strings.Contains(contents, "\nSubsystem ") || strings.Contains(contents, "/home/ubuntu/.ssh/authorized_keys") || strings.Contains(contents, "\nPort 22\n") {
		t.Fatalf("standalone lane sshd config imports or changes global/PRD authority:\n%s", contents)
	}
	unit := l202Artifact(t, plan, ArtifactSSHUnit)
	for _, required := range []string{"User=root", "Group=root", "Slice=" + plan.Namespace.RunnerSlice, "KillMode=control-group", "ExecStart=/usr/sbin/sshd -D -e -f " + plan.SSHDConfigPath, "RuntimeDirectory=runner-sshd-dev"} {
		if !strings.Contains(unit.Contents, required) {
			t.Errorf("listener unit missing %q", required)
		}
	}
	if strings.Contains(unit.Contents, "PAMName=") || strings.Contains(unit.Contents, "Slice=system.slice") {
		t.Fatalf("listener unit can escape its lane slice or use PAM:\n%s", unit.Contents)
	}
}

func TestLanesL202ForcedWrapperAcceptsOnlyFixedCommandAndIdentity(t *testing.T) {
	plan := l202TestPlan(t, "linux-host", config.ReleaseLaneDev)
	artifact := l202Artifact(t, plan, ArtifactSSHWrapper)
	if artifact.Mode != 0o755 || artifact.Owner != "root" || artifact.Group != "root" {
		t.Fatalf("forced wrapper ownership/mode = %+v", artifact)
	}
	temporary := t.TempDir()
	bridge := filepath.Join(temporary, "runner-ssh-bridge")
	controllerMap := filepath.Join(temporary, "ssh-controller-map.yaml")
	socket := filepath.Join(temporary, "runnerd.sock")
	if err := os.WriteFile(bridge, []byte("#!/bin/sh\nprintf 'FIXED=%s\\n' \"$*\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	contents := strings.ReplaceAll(artifact.Contents, filepath.Join(plan.Namespace.Binaries, "runner-ssh-bridge"), bridge)
	contents = strings.ReplaceAll(contents, filepath.Join(plan.Namespace.ServiceRoot, "config", "ssh-controller-map.yaml"), controllerMap)
	contents = strings.ReplaceAll(contents, plan.Namespace.PrivateSocket, socket)
	wrapper := filepath.Join(temporary, "forced.sh")
	if err := os.WriteFile(wrapper, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
	keyFingerprint := opensshFingerprint(mustSSHKey(t, l202PublicKey("dispatcher-linux-host-dev")))
	run := func(argument, original, tty string) ([]byte, error) {
		t.Helper()
		command := exec.Command("/bin/sh", wrapper, argument)
		command.Env = []string{"PATH=/usr/bin:/bin", "SSH_ORIGINAL_COMMAND=" + original}
		if tty != "" {
			command.Env = append(command.Env, "SSH_TTY="+tty)
		}
		return command.CombinedOutput()
	}
	output, err := run(keyFingerprint, "runner-ssh-bridge --stdio", "")
	want := "FIXED=--stdio --authenticated-key " + keyFingerprint + " --controller-map " + controllerMap + " --runnerd-socket " + socket + "\n"
	if err != nil || string(output) != want {
		t.Fatalf("fixed wrapper output=%q err=%v want %q", output, err, want)
	}
	for _, commandText := range []string{"", "sh -c id", "runner-ssh-bridge --stdio --shell", "runner-ssh-bridge --stdio; id", "$(id)"} {
		output, err = run(keyFingerprint, commandText, "")
		if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 126 || !strings.Contains(string(output), "requested command is not permitted") {
			t.Errorf("wrapper accepted unsafe command %q: output=%q err=%v", commandText, output, err)
		}
	}
	output, err = run("SHA256:wrong", "runner-ssh-bridge --stdio", "")
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 126 || !strings.Contains(string(output), "authenticated key identity is not permitted") {
		t.Errorf("wrapper accepted another identity: output=%q err=%v", output, err)
	}
	output, err = run(keyFingerprint, "runner-ssh-bridge --stdio", "/dev/pts/7")
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 126 || !strings.Contains(string(output), "PTY is not permitted") {
		t.Errorf("wrapper accepted PTY: output=%q err=%v", output, err)
	}
}

func TestLanesL202PreservesPort22AndSeparatesClientTrustPerHost(t *testing.T) {
	seenKnownHosts := make(map[string]struct{})
	seenIdentities := make(map[string]struct{})
	for host := range map[string]struct{}{"linux-host": {}, "sandbox-host": {}, "factory-sandbox": {}} {
		for _, lane := range []config.ReleaseLane{config.ReleaseLaneDev, config.ReleaseLaneQAS} {
			plan := l202TestPlan(t, host, lane)
			knownHosts := l202Artifact(t, plan, ArtifactSSHKnownHosts)
			if knownHosts.Owner != config.MacAccount || knownHosts.Group != "staff" || knownHosts.Mode != 0o600 {
				t.Errorf("Mac known_hosts metadata = %+v", knownHosts)
			}
			wantPrefix := fmt.Sprintf("[%s]:%d ssh-ed25519 ", plan.PublicHost, plan.Port)
			if !strings.HasPrefix(knownHosts.Contents, wantPrefix) || !strings.HasSuffix(knownHosts.Contents, "\n") {
				t.Errorf("known_hosts entry = %q, want prefix %q", knownHosts.Contents, wantPrefix)
			}
			if _, exists := seenKnownHosts[plan.ClientKnownHostsPath]; exists {
				t.Errorf("queued profiles share a known_hosts path: %s", plan.ClientKnownHostsPath)
			}
			seenKnownHosts[plan.ClientKnownHostsPath] = struct{}{}
			if _, exists := seenIdentities[plan.ClientDispatcherPrivateKey]; exists {
				t.Errorf("queued profiles share a dispatcher private-key path: %s", plan.ClientDispatcherPrivateKey)
			}
			seenIdentities[plan.ClientDispatcherPrivateKey] = struct{}{}
			if strings.Contains(knownHosts.Contents, "BEGIN OPENSSH PRIVATE KEY") || strings.Contains(knownHosts.Contents, "PRIVATE KEY") {
				t.Fatal("known_hosts artifact contains private material")
			}
		}
	}
}

func TestLanesL202ListenerUsesLaneSliceAndPreventsPAMSessionMigration(t *testing.T) {
	for _, lane := range []config.ReleaseLane{config.ReleaseLaneDev, config.ReleaseLaneQAS} {
		plan := l202TestPlan(t, "sandbox-host", lane)
		unit := l202Artifact(t, plan, ArtifactSSHUnit).Contents
		serverConfig := l202Artifact(t, plan, ArtifactSSHConfig).Contents
		if !strings.Contains(unit, "Slice="+plan.Namespace.RunnerSlice) || !strings.Contains(unit, "KillMode=control-group") || !strings.Contains(serverConfig, "UsePAM no") {
			t.Fatalf("%s listener fixture does not keep the process tree on the lane slice without PAM:\n%s\n%s", lane, unit, serverConfig)
		}
	}
}

func TestLanesL202AuthorizedKeyIsExactRestrictedDispatcherIdentity(t *testing.T) {
	plan := l202TestPlan(t, "factory-sandbox", config.ReleaseLaneQAS)
	keyFields := strings.Fields(l202PublicKey("dispatcher-factory-sandbox-qas"))
	key := strings.Join(keyFields[:2], " ")
	entry := sshdeploy.Entry{PublicKey: key, WrapperPath: plan.ForcedWrapperPath, Comment: "runner-dispatcher-factory-sandbox-qas"}
	want, err := entry.Render()
	if err != nil {
		t.Fatal(err)
	}
	actual := l202Artifact(t, plan, ArtifactSSHKeys)
	if actual.Owner != "root" || actual.Group != "root" || actual.Mode != 0o600 || actual.Contents != want+"\n" {
		t.Fatalf("authorized_keys artifact=%+v want exact restricted line %q", actual, want)
	}
	for _, widened := range []string{
		strings.Replace(want, "restrict,", "", 1),
		strings.Replace(want, `command=`, `command="/bin/sh" `+`command=`, 1),
		strings.Replace(want, plan.ForcedWrapperPath, "/bin/sh", 1),
	} {
		if err := sshdeploy.ValidateRendered(widened, entry); err == nil {
			t.Errorf("widened or altered authorized_keys entry was accepted: %q", widened)
		}
	}
}

func TestLanesL202NativeQueuedSSHConfigValidatesOnSelectedHost(t *testing.T) {
	hostID := os.Getenv("LANES_NATIVE_HOST_ID")
	if hostID == "" {
		if os.Getenv("LANES_NATIVE_VALIDATION") == "1" {
			t.Fatal("LANES_NATIVE_HOST_ID must name this native validation host")
		}
		return
	}
	if hostID == "mac-control-plane" {
		plan := l202TestPlan(t, "linux-host", config.ReleaseLaneDev)
		knownHosts := l202Artifact(t, plan, ArtifactSSHKnownHosts)
		path := filepath.Join(t.TempDir(), "known_hosts")
		if err := os.WriteFile(path, []byte(knownHosts.Contents), 0o600); err != nil {
			t.Fatal(err)
		}
		if output, err := exec.Command("/usr/bin/ssh-keygen", "-lf", path).CombinedOutput(); err != nil {
			t.Fatalf("native Mac OpenSSH known_hosts validation: %v: %s", err, output)
		}
		return
	}
	if runtime.GOOS != "linux" {
		t.Fatalf("native sshd validation requires Linux, got %s for %s", runtime.GOOS, hostID)
	}
	if _, ok := canonicalHosts[hostID]; !ok || hostID == "mac-control-plane" {
		t.Fatalf("unknown native validation host %q", hostID)
	}
	temporary := t.TempDir()
	hostKeyPath := filepath.Join(temporary, "ssh_host_ed25519_key")
	if output, err := exec.Command("/usr/bin/ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", hostKeyPath).CombinedOutput(); err != nil {
		t.Fatalf("generate ephemeral sshd fixture host key: %v: %s", err, output)
	}
	publicKey, err := os.ReadFile(hostKeyPath + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildQueuedSSHPlan(hostID, config.ReleaseLaneDev, l202PublicKey("dispatcher-native-"+hostID), string(publicKey))
	if err != nil {
		t.Fatal(err)
	}
	fixtureAuthority := filepath.Join(temporary, "etc", "remote-session-runner", "ssh", "dev")
	fixtureServiceRoot := filepath.Join(temporary, "home", "ubuntu", ".local", "share", "remote-session-runner-dev")
	fixtureRuntime := filepath.Join(temporary, "run", "runner-sshd-dev")
	fixtureKnownHosts := filepath.Join(temporary, "mac", "known_hosts")
	fixtureUnits := filepath.Join(temporary, "etc", "systemd", "system")
	for _, directory := range []string{fixtureAuthority, fixtureServiceRoot, fixtureRuntime, fixtureKnownHosts[:strings.LastIndex(fixtureKnownHosts, "/")], fixtureUnits} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Rename(hostKeyPath, filepath.Join(fixtureAuthority, "ssh_host_ed25519_key")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(hostKeyPath+".pub", filepath.Join(fixtureAuthority, "ssh_host_ed25519_key.pub")); err != nil {
		t.Fatal(err)
	}
	var configPath, servicePath string
	for _, artifact := range plan.Artifacts {
		contents := strings.ReplaceAll(artifact.Contents, plan.Namespace.SSHAuthorityPath, fixtureAuthority)
		contents = strings.ReplaceAll(contents, plan.Namespace.ServiceRoot, fixtureServiceRoot)
		contents = strings.ReplaceAll(contents, plan.RuntimeDirectory, fixtureRuntime)
		var path string
		switch artifact.Kind {
		case ArtifactSSHConfig:
			path = filepath.Join(fixtureAuthority, "sshd_config")
			configPath = path
		case ArtifactSSHKeys:
			path = filepath.Join(fixtureAuthority, "authorized_keys")
		case ArtifactSSHWrapper:
			path = filepath.Join(fixtureAuthority, "runner-ssh-bridge-forced.sh")
		case ArtifactSSHUnit:
			path = filepath.Join(fixtureUnits, filepath.Base(artifact.InstallPath))
			contents = strings.ReplaceAll(contents, plan.SSHDConfigPath, configPath)
			servicePath = path
		case ArtifactSSHKnownHosts:
			path = fixtureKnownHosts
		}
		if err := os.WriteFile(path, []byte(contents), os.FileMode(artifact.Mode)); err != nil {
			t.Fatal(err)
		}
	}
	basePlan, err := BuildPlan(hostID, config.ReleaseLaneDev)
	if err != nil {
		t.Fatal(err)
	}
	var slicePath string
	for _, artifact := range basePlan.Artifacts {
		if artifact.Kind != ArtifactSystemdUnit || !strings.HasSuffix(artifact.InstallPath, ".slice") {
			continue
		}
		slicePath = filepath.Join(fixtureUnits, filepath.Base(artifact.InstallPath))
		if err := os.WriteFile(slicePath, []byte(artifact.Contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if configPath == "" || servicePath == "" || slicePath == "" {
		t.Fatal("native fixture is missing sshd configuration or service/slice unit")
	}
	if output, err := exec.Command("/usr/sbin/sshd", "-t", "-f", configPath).CombinedOutput(); err != nil {
		t.Fatalf("native sshd -t: %v: %s", err, output)
	}
	effective, err := exec.Command("/usr/sbin/sshd", "-T", "-C", "user=ubuntu,host=localhost,addr=127.0.0.1", "-f", configPath).CombinedOutput()
	if err != nil {
		t.Fatalf("native sshd -T: %v: %s", err, effective)
	}
	for _, required := range []string{"port 2224", "usepam no", "permittty no", "allowtcpforwarding no", "allowagentforwarding no", "x11forwarding no", "maxsessions 1", "forcecommand " + filepath.Join(fixtureAuthority, "runner-ssh-bridge-forced.sh")} {
		if !strings.Contains(string(effective), required) {
			t.Errorf("native sshd effective configuration missing %q:\n%s", required, effective)
		}
	}
	if output, err := exec.Command("/usr/bin/systemd-analyze", "verify", servicePath, slicePath).CombinedOutput(); err != nil {
		t.Fatalf("native systemd queued listener/slice validation: %v: %s", err, output)
	}
}

func l202TestPlan(t *testing.T, host string, lane config.ReleaseLane) QueuedSSHPlan {
	t.Helper()
	plan, err := BuildQueuedSSHPlan(host, lane, l202PublicKey("dispatcher-"+host+"-"+string(lane)), l202PublicKey("host-"+host+"-"+string(lane)))
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func l202Artifact(t *testing.T, plan QueuedSSHPlan, kind ArtifactKind) Artifact {
	t.Helper()
	for _, artifact := range plan.Artifacts {
		if artifact.Kind == kind {
			return artifact
		}
	}
	t.Fatalf("%s/%s plan has no artifact kind %s", plan.HostID, plan.Lane, kind)
	return Artifact{}
}

func l202PublicKey(identity string) string {
	seed := sha256.Sum256([]byte(identity))
	private := ed25519.NewKeyFromSeed(seed[:])
	public := private.Public().(ed25519.PublicKey)
	var blob bytes.Buffer
	_ = binary.Write(&blob, binary.BigEndian, uint32(len("ssh-ed25519")))
	blob.WriteString("ssh-ed25519")
	_ = binary.Write(&blob, binary.BigEndian, uint32(len(public)))
	blob.Write(public)
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob.Bytes()) + " fixture"
}

func mustSSHKey(t *testing.T, public string) []byte {
	t.Helper()
	parsed, err := parseEd25519PublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.blob
}
