package laneinstall

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"remote-session-runner/src/internal/config"
)

func TestLanesL201BuildsFixedDevQASPlansForAllHosts(t *testing.T) {
	hosts := []string{"mac-control-plane", "linux-host", "sandbox-host", "factory-sandbox"}
	lanes := []config.ReleaseLane{config.ReleaseLaneDev, config.ReleaseLaneQAS}
	for _, host := range hosts {
		for _, lane := range lanes {
			plan, err := BuildPlan(host, lane)
			if err != nil {
				t.Fatalf("BuildPlan(%s, %s): %v", host, lane, err)
			}
			if plan.HostID != host || plan.Lane != lane || plan.Namespace.Lane != lane || len(plan.Artifacts) == 0 {
				t.Fatalf("plan identity/artifacts = %+v", plan)
			}
			if !strings.HasSuffix(plan.Namespace.ServiceRoot, "-"+string(lane)) {
				t.Fatalf("%s %s selected unsuffixed root %q", host, lane, plan.Namespace.ServiceRoot)
			}
			for _, artifact := range plan.Artifacts {
				if artifact.InstallPath == "" || artifact.Contents == "" || strings.Contains(artifact.InstallPath, "RemoteSessionRunner/") || strings.Contains(artifact.Contents, "RemoteSessionRunner/") {
					t.Fatalf("%s %s emitted an incomplete or unsuffixed artifact: %+v", host, lane, artifact)
				}
			}
			if host == "mac-control-plane" {
				if len(plan.Artifacts) != 2 || plan.Namespace.Host != config.HostKindMac || plan.Namespace.Account != config.MacAccount {
					t.Fatalf("Mac plan = %+v", plan)
				}
				if plan.Artifacts[0].InstallPath != plan.Namespace.LocalAgentPlist || plan.Artifacts[1].InstallPath != plan.Namespace.LocalDAgentPlist {
					t.Fatalf("Mac install destinations differ from the fixed namespace: %+v", plan.Artifacts)
				}
			} else if len(plan.Artifacts) != 3 || plan.Namespace.Host != config.HostKindLinux || plan.Namespace.RemoteProfile != host || plan.Namespace.Account != config.LinuxAccount {
				t.Fatalf("Linux plan %s = %+v", host, plan)
			} else {
				wantPaths := []string{
					filepath.Join(plan.Namespace.Binaries, "runnerd-entrypoint.sh"),
					filepath.Join("/etc/systemd/system", plan.Namespace.RunnerUnit),
					filepath.Join("/etc/systemd/system", plan.Namespace.RunnerSlice),
				}
				for i, want := range wantPaths {
					if plan.Artifacts[i].InstallPath != want {
						t.Fatalf("Linux %s artifact[%d] destination=%q want %q", host, i, plan.Artifacts[i].InstallPath, want)
					}
				}
			}
			for _, artifact := range plan.Artifacts {
				if plan.Namespace.MailboxRoot != "" && (strings.Contains(artifact.InstallPath, plan.Namespace.MailboxRoot) || strings.Contains(artifact.Contents, plan.Namespace.MailboxRoot)) {
					t.Fatalf("%s %s installer artifact touches an external mailbox root: %+v", host, lane, artifact)
				}
				for _, forbidden := range []string{"launchctl", "systemctl", "mount ", "rm -", "mkdir "} {
					if strings.Contains(artifact.Contents, forbidden) {
						t.Fatalf("%s %s template contains a live or destructive action %q", host, lane, forbidden)
					}
				}
			}
		}
	}
}

func TestLanesL201RejectsPRDAndUnknownHost(t *testing.T) {
	if _, err := BuildPlan("mac-control-plane", config.ReleaseLanePRD); !errors.Is(err, ErrInvalidLane) {
		t.Fatalf("PRD plan error = %v, want %v", err, ErrInvalidLane)
	}
	for _, host := range []string{"", "ubuntu", "linux-host-dev", "unknown-host"} {
		if _, err := BuildPlan(host, config.ReleaseLaneDev); !errors.Is(err, ErrInvalidHost) {
			t.Errorf("BuildPlan(%q) error = %v, want %v", host, err, ErrInvalidHost)
		}
	}
	if _, err := ManagementPackage("unknown-host"); !errors.Is(err, ErrInvalidHost) {
		t.Fatalf("unknown management host error = %v, want %v", err, ErrInvalidHost)
	}
}

func TestLanesL201RendersOnlyFixedLaneServiceIdentities(t *testing.T) {
	for _, lane := range []config.ReleaseLane{config.ReleaseLaneDev, config.ReleaseLaneQAS} {
		mac, err := BuildPlan("mac-control-plane", lane)
		if err != nil {
			t.Fatal(err)
		}
		for _, artifact := range mac.Artifacts {
			if artifact.Kind != ArtifactLaunchAgent || !strings.Contains(artifact.InstallPath, "com.remote-session-runner.") || !strings.Contains(artifact.InstallPath, "-"+string(lane)+".plist") || !strings.Contains(artifact.Contents, "<key>StandardOutPath</key>\n\t<string>/dev/null</string>") || !strings.Contains(artifact.Contents, "<key>StandardErrorPath</key>\n\t<string>/dev/null</string>") || !strings.Contains(artifact.Contents, "--stdout-path") || !strings.Contains(artifact.Contents, "--stderr-path") {
				t.Fatalf("Mac %s agent artifact = %+v", lane, artifact)
			}
			if strings.Contains(artifact.Contents, "<key>UserName</key>") {
				t.Fatalf("LaunchAgent changes the logged-in owner identity: %s", artifact.InstallPath)
			}
		}

		linux, err := BuildPlan("linux-host", lane)
		if err != nil {
			t.Fatal(err)
		}
		var service, entrypoint string
		for _, artifact := range linux.Artifacts {
			switch artifact.Kind {
			case ArtifactSystemdUnit:
				if strings.HasSuffix(artifact.InstallPath, ".service") {
					service = artifact.Contents
				}
			case ArtifactEntrypoint:
				entrypoint = artifact.Contents
			}
		}
		for _, required := range []string{"User=ubuntu", "Group=ubuntu", "StandardOutput=null", "StandardError=null", "KillMode=mixed", "TimeoutStopSec=30s", "Slice=remote-session-runner-" + string(lane) + ".slice", "--stdout-path", "--stderr-path"} {
			if !strings.Contains(service, required) {
				t.Errorf("Linux %s service missing %q", lane, required)
			}
		}
		for _, required := range []string{"expected_account='ubuntu'", "/remote-session-runner-" + string(lane), "check_file \"$service_root/bin/runner-log-capture\" 700", "exec \"$service_root/bin/runnerd\" --config \"$service_root/config/linux.yaml\""} {
			if !strings.Contains(entrypoint, required) {
				t.Errorf("Linux %s entrypoint missing %q", lane, required)
			}
		}
		if strings.Contains(service, "StandardOutput=journal") || strings.Contains(entrypoint, "/remote-session-runner'") {
			t.Fatalf("Linux %s artifact can spill logs or target unsuffixed PRD", lane)
		}
	}
}

func TestLanesL201ManagementPackagesMatchClosedHostMatrix(t *testing.T) {
	want := map[string][]string{
		"mac-control-plane": {"runner-release-builder", "runner-release-transport", "runner-security-anchor-helper", "runner-release-authority-signer", "runner-release-authority-agent", "runner-release-authority-approval", "runner-release-transport-pki-issuer", "runner-release-transport-pki-installer"},
		"linux-host":        {"runner-release-builder", "runner-release-transport", "runner-security-anchor-helper", "runner-release-transport-pki-installer"},
		"sandbox-host":      {"runner-release-builder", "runner-release-transport", "runner-security-anchor-helper", "runner-release-transport-pki-installer"},
		"factory-sandbox":   {"runner-release-builder", "runner-release-transport", "runner-security-anchor-helper", "runner-release-transport-pki-installer"},
	}
	for host, names := range want {
		pkg, err := ManagementPackage(host)
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateManagementPackage(pkg); err != nil {
			t.Fatalf("%s management package: %v", host, err)
		}
		if len(pkg.Components) != len(names) {
			t.Fatalf("%s has %d components, want %d", host, len(pkg.Components), len(names))
		}
		for i, name := range names {
			if pkg.Components[i].Name != name {
				t.Fatalf("%s component[%d]=%s want %s", host, i, pkg.Components[i].Name, name)
			}
		}
		if pkg.Components[0].Identity != "unprivileged sealed-staging builder" {
			t.Fatalf("%s builder identity = %q", host, pkg.Components[0].Identity)
		}
		if host == "mac-control-plane" && (pkg.Components[3].Identity != "root launchd signer" || pkg.Components[4].Identity != "root launchd authority agent" || pkg.Components[5].Identity != "interactive owner approval tool") {
			t.Fatalf("Mac authority component identities = %+v", pkg.Components[3:6])
		}
		if host != "mac-control-plane" && (pkg.Components[1].Identity != "dedicated non-login runner-release-transport account" || pkg.Components[3].Identity != "root constrained local-key installer") {
			t.Fatalf("%s transport/installer identities = %+v", host, pkg.Components[1:])
		}
		if !pkg.Protected.SecurityAnchorStatePreserved || !pkg.Protected.AuthorityPublicAnchorPreserved || pkg.Protected.LaneArtifactsMayReplaceMgmt {
			t.Fatalf("%s protected state policy = %+v", host, pkg.Protected)
		}
		if host == "mac-control-plane" && !pkg.Protected.AuthorityStatePreserved {
			t.Fatal("Mac host-management update does not preserve authority state")
		}
	}
}

func TestLanesL201SensitiveKeyAndPeerBoundariesAreSeparated(t *testing.T) {
	mac, err := ManagementPackage("mac-control-plane")
	if err != nil {
		t.Fatal(err)
	}
	assertAsset := func(pkg HostManagementPackage, name, owner, mode string, readers ...string) AssetPolicy {
		t.Helper()
		for _, asset := range pkg.Assets {
			if asset.Name == name {
				if asset.OwnerComponent != owner || asset.Mode != mode || !equalStrings(asset.ReaderComponents, readers) {
					t.Fatalf("%s asset boundary = %+v", name, asset)
				}
				if !asset.PreserveOnUpdate {
					t.Fatalf("%s is not preserved on management update", name)
				}
				return asset
			}
		}
		t.Fatalf("missing asset policy %s", name)
		return AssetPolicy{}
	}
	assertSocket := func(pkg HostManagementPackage, name, peer, identity string) SocketPolicy {
		t.Helper()
		for _, socket := range pkg.Sockets {
			if socket.Name == name {
				if socket.PeerComponent != peer || socket.PeerIdentity != identity {
					t.Fatalf("%s peer policy = %+v", name, socket)
				}
				return socket
			}
		}
		t.Fatalf("missing socket policy %s", name)
		return SocketPolicy{}
	}
	assertAsset(mac, "release-authority-private-key", "runner-release-authority-signer", "0600", "runner-release-authority-signer")
	assertAsset(mac, "release-authority-state", "runner-release-authority-signer", "0700", "runner-release-authority-signer")
	assertAsset(mac, "release-transport-ca-private-key", "runner-release-transport-pki-issuer", "0600", "runner-release-transport-pki-issuer")
	assertAsset(mac, "release-transport-client-leaf-private-key", "runner-release-transport-pki-installer", "", "runner-release-authority-agent")
	for _, asset := range mac.Assets {
		if asset.Name == "release-transport-client-leaf-private-key" && asset.ReadBoundary != "authority-agent-code-identity-only" {
			t.Fatalf("Mac client leaf read boundary = %q", asset.ReadBoundary)
		}
	}
	assertAsset(mac, "host-attestation-private-key", "runner-security-anchor-helper", "0600", "runner-security-anchor-helper")
	assertSocket(mac, "security-anchor-authority-state", "runner-release-authority-agent", "launchd audit token, label, and code identity")
	assertSocket(mac, "release-authority-signing", "runner-release-authority-agent", "launchd audit token, label, and code identity")
	assertSocket(mac, "security-anchor-observe", "runner-service", "tomasz.walczuk")
	for _, socket := range mac.Sockets {
		if socket.PeerComponent == "runner-release-authority-approval" {
			t.Fatal("interactive approval tool receives signer or anchor socket access")
		}
	}

	for _, host := range []string{"linux-host", "sandbox-host", "factory-sandbox"} {
		pkg, err := ManagementPackage(host)
		if err != nil {
			t.Fatal(err)
		}
		assertAsset(pkg, "security-anchor-state", "runner-security-anchor-helper", "0700", "runner-security-anchor-helper")
		assertAsset(pkg, "release-transport-server-leaf-private-key", "runner-release-transport", "0600", "runner-release-transport")
		assertSocket(pkg, "security-anchor-observe", "runner-service", "ubuntu")
		assertSocket(pkg, "security-anchor-release-transport", "runner-release-transport", "measured transport identity")
		for _, asset := range pkg.Assets {
			if asset.Name == "release-transport-ca-private-key" || asset.Name == "release-authority-private-key" {
				t.Fatalf("%s package has an unauthorized authority key: %+v", host, asset)
			}
		}
	}
}

func TestLanesL201ManagementPackageRejectsBoundaryDrift(t *testing.T) {
	pkg, err := ManagementPackage("mac-control-plane")
	if err != nil {
		t.Fatal(err)
	}
	pkg.Assets[0].ReaderComponents = []string{"runner-release-builder"}
	if err := ValidateManagementPackage(pkg); err == nil {
		t.Fatal("package with reassigned host attestation key was accepted")
	}
	pkg, err = ManagementPackage("mac-control-plane")
	if err != nil {
		t.Fatal(err)
	}
	pkg.Sockets[len(pkg.Sockets)-1].PeerComponent = "runner-release-authority-approval"
	if err := ValidateManagementPackage(pkg); err == nil {
		t.Fatal("package granting signer access to approval tool was accepted")
	}
}

func TestLanesL201NativeServiceTemplatesValidateOnSelectedHost(t *testing.T) {
	hostID := os.Getenv("LANES_NATIVE_HOST_ID")
	if hostID == "" {
		if os.Getenv("LANES_NATIVE_VALIDATION") == "1" {
			t.Fatal("LANES_NATIVE_HOST_ID must name this native validation host")
		}
		return
	}
	for _, lane := range []config.ReleaseLane{config.ReleaseLaneDev, config.ReleaseLaneQAS} {
		plan, err := BuildPlan(hostID, lane)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS == "darwin" {
			if plan.Namespace.Host != config.HostKindMac {
				t.Fatalf("Darwin native validation received non-Mac host %s", hostID)
			}
			for i, artifact := range plan.Artifacts {
				if artifact.Kind != ArtifactLaunchAgent {
					continue
				}
				path := filepath.Join(t.TempDir(), filepath.Base(artifact.InstallPath))
				if err := os.WriteFile(path, []byte(artifact.Contents), 0o600); err != nil {
					t.Fatal(err)
				}
				command := exec.Command("/usr/bin/plutil", "-lint", path)
				if output, err := command.CombinedOutput(); err != nil {
					t.Fatalf("plutil native validation %s artifact %d: %v: %s", lane, i, err, output)
				}
			}
			continue
		}
		if runtime.GOOS != "linux" || plan.Namespace.Host != config.HostKindLinux {
			t.Fatalf("native validator does not support host os=%s id=%s", runtime.GOOS, hostID)
		}
		if _, err := os.Stat("/usr/bin/systemd-analyze"); err != nil {
			t.Fatalf("native systemd analyzer unavailable: %v", err)
		}

		fixtureRoot := t.TempDir()
		serviceRoot := filepath.Join(fixtureRoot, "home", "ubuntu", ".local", "share", filepath.Base(plan.Namespace.ServiceRoot))
		unitRoot := filepath.Join(fixtureRoot, "etc", "systemd", "system")
		binaryRoot := filepath.Join(serviceRoot, "bin")
		if err := os.MkdirAll(binaryRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(unitRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, artifact := range plan.Artifacts {
			if artifact.Kind == ArtifactEntrypoint {
				contents := strings.ReplaceAll(artifact.Contents, plan.Namespace.ServiceRoot, serviceRoot)
				path := filepath.Join(binaryRoot, filepath.Base(artifact.InstallPath))
				if err := os.WriteFile(path, []byte(contents), os.FileMode(artifact.Mode)); err != nil {
					t.Fatal(err)
				}
				if output, err := exec.Command("/bin/sh", "-n", path).CombinedOutput(); err != nil {
					t.Fatalf("lane entrypoint shell syntax %s: %v: %s", lane, err, output)
				}
			}
		}
		for _, name := range []string{"runner-log-capture", "runnerd"} {
			path := filepath.Join(binaryRoot, name)
			if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		var verifyFiles []string
		for _, artifact := range plan.Artifacts {
			if artifact.Kind != ArtifactSystemdUnit {
				continue
			}
			name := filepath.Base(artifact.InstallPath)
			path := filepath.Join(unitRoot, name)
			contents := strings.ReplaceAll(artifact.Contents, plan.Namespace.ServiceRoot, serviceRoot)
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			verifyFiles = append(verifyFiles, filepath.Join("etc", "systemd", "system", name))
		}
		for _, target := range []string{"network-online.target", "multi-user.target"} {
			stub := filepath.Join(unitRoot, target)
			if err := os.WriteFile(stub, []byte("[Unit]\nDescription=isolated lane template validation fixture\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			verifyFiles = append(verifyFiles, filepath.Join("etc", "systemd", "system", target))
		}
		args := append([]string{"--root=" + fixtureRoot, "verify"}, verifyFiles...)
		command := exec.Command("/usr/bin/systemd-analyze", args...)
		command.Dir = fixtureRoot
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("systemd-analyze native validation %s: %v: %s", lane, err, output)
		}
	}
}
