package laneinstall

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/domain"
)

func TestSimpleQueuedSSHUsesExistingPort22AndLaneBoundForcedCommand(t *testing.T) {
	for _, host := range []string{"linux-host", "sandbox-host", "factory-sandbox"} {
		for _, lane := range []config.ReleaseLane{config.ReleaseLaneDev, config.ReleaseLaneQAS} {
			plan, err := BuildSimpleQueuedSSHPlan(host, lane, l202PublicKey("simple-dispatcher-"+host+"-"+string(lane)), l202PublicKey("existing-host-key-"+host))
			if err != nil {
				t.Fatalf("BuildSimpleQueuedSSHPlan(%s/%s): %v", host, lane, err)
			}
			if plan.Port != 22 || plan.Profile != host || plan.PublicHost == "" ||
				plan.AuthorizedKeysPath != "/home/ubuntu/.ssh/authorized_keys" ||
				strings.Contains(plan.ForcedWrapperPath, "2224") || strings.Contains(plan.ForcedWrapperPath, "2225") {
				t.Fatalf("simple queued route is not bound to the existing listener: %+v", plan)
			}
			wrapper := plan.LinuxArtifacts[0].Contents
			for _, required := range []string{"runner-ssh-bridge --stdio", "SSH_ORIGINAL_COMMAND", "SSH_TTY", "--authenticated-key", plan.ControllerMapPath, "--runnerd-socket", "current/bin/runner-ssh-bridge", "--catalog-host", host, "--catalog-lane", string(lane)} {
				if !strings.Contains(wrapper, required) {
					t.Errorf("%s/%s forced wrapper omits %q", host, lane, required)
				}
			}
			for _, forbidden := range []string{"/etc/ssh/sshd_config", "systemctl", "sshd -D", "--shell", "Slice="} {
				if strings.Contains(wrapper, forbidden) {
					t.Errorf("%s/%s forced wrapper contains excluded behavior %q", host, lane, forbidden)
				}
			}
			if !strings.HasPrefix(plan.LinuxArtifacts[2].Contents, `restrict,command="`+plan.ForcedWrapperPath+" ") ||
				!strings.HasSuffix(plan.LinuxArtifacts[2].Contents, "\n") ||
				!strings.Contains(plan.LinuxArtifacts[1].Contents, "controller_type: queued_mac") ||
				!strings.Contains(plan.LinuxArtifacts[1].Contents, "controller_id: \""+config.MacAccount+"\"") {
				t.Fatalf("lane key mapping or fixed wrapper command is malformed: %+v", plan.LinuxArtifacts)
			}
			if !strings.HasPrefix(plan.MacArtifacts[0].Contents, plan.PublicHost+" ssh-ed25519 ") || strings.HasPrefix(plan.MacArtifacts[0].Contents, "[") {
				t.Fatalf("known_hosts does not use OpenSSH's default-port host token for port 22: %q", plan.MacArtifacts[0].Contents)
			}
		}
	}
}

func TestSimpleQueuedSSHUsesValidatedReplacementV3Address(t *testing.T) {
	dispatcherKey := l202PublicKey("replacement-dispatcher")
	hostKey := l202PublicKey("replacement-existing-host")
	mac, linux := simpleQueuedReplacementProfiles(t, config.ReleaseLaneDev, dispatcherKey, hostKey)
	plan, err := BuildSimpleQueuedSSHPlanFromV3Profiles(
		"linux-host", config.ReleaseLaneDev, dispatcherKey, hostKey, mac, linux,
	)
	if err != nil {
		t.Fatalf("build queued plan from explicit V3 route: %v", err)
	}
	if err := plan.ValidateProfiles(mac, linux); err != nil {
		t.Fatalf("replacement route did not match the paired worker profile: %v", err)
	}
	if plan.PublicHost != "79.72.94.59" || !strings.HasPrefix(plan.MacArtifacts[0].Contents, "79.72.94.59 ssh-ed25519 ") {
		t.Fatalf("known_hosts route did not follow the validated V3 profile: host=%q contents=%q", plan.PublicHost, plan.MacArtifacts[0].Contents)
	}

	legacy, err := BuildSimpleQueuedSSHPlan("linux-host", config.ReleaseLaneDev, l202PublicKey("legacy-dispatcher"), l202PublicKey("legacy-host"))
	if err != nil || legacy.PublicHost != "129.151.232.40" {
		t.Fatalf("legacy default route changed: plan=%+v err=%v", legacy, err)
	}
	if _, err := BuildSimpleQueuedSSHPlanFromV3Profiles(
		"linux-host", config.ReleaseLanePRD, l202PublicKey("prd-dispatcher"),
		l202PublicKey("prd-host"), mac, linux,
	); err == nil {
		t.Fatal("replacement Dev/QAS address was accepted for PRD")
	}
}

func simpleQueuedReplacementProfiles(t *testing.T, lane config.ReleaseLane, dispatcherPublicKey, hostPublicKey string) (config.ValidatedV3Profiles, config.ValidatedV3Profiles) {
	t.Helper()
	dispatcher, err := parseEd25519PublicKey(dispatcherPublicKey)
	if err != nil {
		t.Fatal(err)
	}
	hostKey, err := parseEd25519PublicKey(hostPublicKey)
	if err != nil {
		t.Fatal(err)
	}
	macNS, err := config.DeriveSimpleLaneNamespace(config.HostKindMac, lane, "")
	if err != nil {
		t.Fatal(err)
	}
	linuxNS, err := config.DeriveSimpleLaneNamespace(config.HostKindLinux, lane, "linux-host")
	if err != nil {
		t.Fatal(err)
	}
	profiles := make([]config.V3MacRemoteProfile, 0, 3)
	allTargets := make([]config.V3TargetBinding, 0, 3)
	allContexts := make([]config.V3ExecutionContextBinding, 0, 3)
	controllers := []config.V3ControllerBinding{{Type: string(domain.ControllerTypeQueuedMac), ID: config.MacAccount}}
	for _, host := range []string{"linux-host", "sandbox-host", "factory-sandbox"} {
		ns, err := config.DeriveSimpleLaneNamespace(config.HostKindLinux, lane, host)
		if err != nil {
			t.Fatal(err)
		}
		publicIP := map[string]string{"linux-host": "129.151.232.40", "sandbox-host": "132.226.205.205", "factory-sandbox": "145.241.255.66"}[host]
		if host == "linux-host" && lane != config.ReleaseLanePRD {
			publicIP = "79.72.94.59"
		}
		port := ns.DirectPort
		fp := func(offset int) string {
			sum := sha256.Sum256([]byte(string(lane) + "/" + host + "/" + fmt.Sprint(offset)))
			return "sha256:" + hex.EncodeToString(sum[:])
		}
		hostFingerprint, dispatcherFingerprint := fp(0), fp(1)
		if host == "linux-host" {
			hostFingerprint = lowerHexFingerprint(hostKey.blob)
			dispatcherFingerprint = lowerHexFingerprint(dispatcher.blob)
		}
		profile := config.V3MacRemoteProfile{
			Profile: host,
			Queued: config.V3MacQueuedCredentials{
				Host: publicIP, Port: 22,
				KnownHostsFile:     filepath.Join(macNS.ServiceRoot, "secrets", "profiles", host, "ssh", "known_hosts"),
				HostKeyFingerprint: hostFingerprint, DispatcherPrivateKeyFile: filepath.Join(macNS.ServiceRoot, "secrets", "profiles", host, "ssh", "dispatcher_ed25519"),
				DispatcherPublicKeyFingerprint: dispatcherFingerprint,
			},
			Direct: config.V3MacDirectCredentials{
				Name: ns.DirectEndpointName, URL: "https://" + publicIP + ":" + fmt.Sprint(port),
				ServerCAFile: filepath.Join(macNS.ServiceRoot, "secrets", "profiles", host, "tls", "server-ca.pem"), ServerCAFingerprint: fp(2),
				ClientCertificateFile: filepath.Join(macNS.ServiceRoot, "secrets", "profiles", host, "tls", "client.pem"), ClientCertificateFingerprint: fp(3),
				ClientPrivateKeyFile: filepath.Join(macNS.ServiceRoot, "secrets", "profiles", host, "tls", "client.key"), ClientSPKIFingerprint: fp(4),
				ExpectedServerCertificateFingerprint: fp(5), ExpectedClientCAFingerprint: fp(6),
				ClientURISAN:   "urn:remote-session-runner:lane:" + string(lane) + ":controller:" + host,
				ServerURISAN:   "urn:remote-session-runner:lane:" + string(lane) + ":server:" + host,
				ControllerType: string(domain.ControllerTypeDirectMTLS), ControllerID: "runner-controller-" + string(lane) + "-" + host,
			},
		}
		profiles = append(profiles, profile)
		target := config.V3TargetBinding{Kind: domain.TargetKindRemote, Profile: host}
		allTargets = append(allTargets, target)
		allContexts = append(allContexts, config.V3ExecutionContextBinding{Name: "ctx-" + host, Environment: "runner", Target: target})
		controllers = append(controllers, config.V3ControllerBinding{Type: string(domain.ControllerTypeDirectMTLS), ID: profile.Direct.ControllerID})
	}
	macBindings := config.V3ProfileBindings{
		Version: config.VersionV3, ReleaseLane: lane, Host: config.HostKindMac, MacProfiles: profiles,
		Environments:      []config.V3EnvironmentBinding{{Name: "runner", EffectiveAccount: config.LinuxAccount, AllowedTargets: allTargets, AllowedControllers: controllers}},
		ExecutionContexts: allContexts,
	}
	macProfiles, err := config.ValidateSimpleV3ProfileBindings(macBindings)
	if err != nil {
		t.Fatalf("validate Mac replacement profiles: %v", err)
	}
	macRoute, _ := macProfiles.MacProfile("linux-host")
	linuxBindings := config.V3ProfileBindings{
		Version: config.VersionV3, ReleaseLane: lane, Host: config.HostKindLinux, RemoteProfile: "linux-host",
		LinuxProfile: &config.V3LinuxProfileBinding{
			RemoteProfile: "linux-host",
			Queued: config.V3LinuxQueuedCredentials{
				Host: macRoute.Queued.Host, Port: 22, Bind: linuxNS.QueuedSSHBind,
				HostKeyFile: linuxNS.SSHHostPublicKeyFile, HostKeyFingerprint: macRoute.Queued.HostKeyFingerprint,
				AuthorizedKeyFingerprint: macRoute.Queued.DispatcherPublicKeyFingerprint, Socket: linuxNS.PrivateSocket,
			},
			Direct: config.V3LinuxDirectCredentials{
				Name: macRoute.Direct.Name, URL: macRoute.Direct.URL, Bind: func() string {
					ip := "10.0.0.200"
					if lane != config.ReleaseLanePRD {
						ip = "10.0.0.228"
					}
					return ip + ":" + fmt.Sprint(linuxNS.DirectPort)
				}(),
				ServerCertificateFile:           filepath.Join(linuxNS.ServiceRoot, "secrets", "profiles", "linux-host", "tls", "server.pem"),
				ServerCertificateFingerprint:    macRoute.Direct.ExpectedServerCertificateFingerprint,
				ServerPrivateKeyFile:            filepath.Join(linuxNS.ServiceRoot, "secrets", "profiles", "linux-host", "tls", "server.key"),
				ServerSPKIFingerprint:           fpFor("server-spki"),
				ClientCAFile:                    filepath.Join(linuxNS.ServiceRoot, "secrets", "profiles", "linux-host", "tls", "client-ca.pem"),
				ClientCAFingerprint:             macRoute.Direct.ExpectedClientCAFingerprint,
				ClientPrincipalMapFile:          filepath.Join(linuxNS.ServiceRoot, "config", "client-principals.yaml"),
				AuthorizedClientFingerprint:     macRoute.Direct.ClientCertificateFingerprint,
				AuthorizedClientSPKIFingerprint: macRoute.Direct.ClientSPKIFingerprint,
				ClientURISAN:                    macRoute.Direct.ClientURISAN, ServerURISAN: macRoute.Direct.ServerURISAN,
				ControllerType: macRoute.Direct.ControllerType, ControllerID: macRoute.Direct.ControllerID,
			},
		},
		Environments: []config.V3EnvironmentBinding{{Name: "runner", EffectiveAccount: config.LinuxAccount,
			AllowedTargets: []config.V3TargetBinding{{Kind: domain.TargetKindRemote, Profile: "linux-host"}},
			AllowedControllers: []config.V3ControllerBinding{
				{Type: string(domain.ControllerTypeQueuedMac), ID: config.MacAccount},
				{Type: string(domain.ControllerTypeDirectMTLS), ID: macRoute.Direct.ControllerID},
			}}},
		ExecutionContexts: []config.V3ExecutionContextBinding{{Name: "ctx-linux-host", Environment: "runner", Target: config.V3TargetBinding{Kind: domain.TargetKindRemote, Profile: "linux-host"}}},
	}
	linuxProfiles, err := config.ValidateSimpleV3ProfileBindings(linuxBindings)
	if err != nil {
		t.Fatalf("validate Linux replacement profile: %v", err)
	}
	return macProfiles, linuxProfiles
}

func fpFor(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestSimpleQueuedAuthorizedKeyAppendPreservesAndBacksUpExistingEntries(t *testing.T) {
	root := simpleTestTempDir(t)
	sshDirectory := filepath.Join(root, ".ssh")
	backups := filepath.Join(root, "backups")
	if err := os.Mkdir(sshDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(backups, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sshDirectory, "authorized_keys")
	existing := []byte("# retained production keys\nrestrict,command=\"/existing/prd-wrapper SHA256:old\" ssh-ed25519 AAAAold prd-key\nssh-ed25519 AAAAunrelated unrelated\n")
	if err := os.WriteFile(path, existing, 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildSimpleQueuedSSHPlan("linux-host", config.ReleaseLaneDev, l202PublicKey("append-dispatcher"), l202PublicKey("existing-ssh-host"))
	if err != nil {
		t.Fatal(err)
	}
	line := plan.LinuxArtifacts[2].Contents
	if err := appendSimpleAuthorizedKeyAt(path, line, backups); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(updated), string(existing)) || !strings.HasSuffix(string(updated), line) {
		t.Fatalf("append changed or dropped existing SSH entries:\n%s", updated)
	}
	entries, err := os.ReadDir(backups)
	if err != nil || len(entries) != 1 {
		t.Fatalf("authorized_keys backup entries=%v err=%v", entries, err)
	}
	backup, err := os.ReadFile(filepath.Join(backups, entries[0].Name()))
	if err != nil || string(backup) != string(existing) {
		t.Fatalf("authorized_keys backup does not preserve original bytes: %q err=%v", backup, err)
	}
	if err := appendSimpleAuthorizedKeyAt(path, line, backups); err != nil {
		t.Fatalf("identical route retry failed: %v", err)
	}
	updatedAgain, _ := os.ReadFile(path)
	if string(updatedAgain) != string(updated) {
		t.Fatal("idempotent retry duplicated the lane key")
	}
}

func TestSimpleQueuedAuthorizedKeyAppendRejectsWidenedSameIdentityAndSymlink(t *testing.T) {
	root := simpleTestTempDir(t)
	sshDirectory := filepath.Join(root, ".ssh")
	backups := filepath.Join(root, "backups")
	if err := os.Mkdir(sshDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(backups, 0o700); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildSimpleQueuedSSHPlan("factory-sandbox", config.ReleaseLaneQAS, l202PublicKey("widened-dispatcher"), l202PublicKey("existing-ssh-host"))
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSuffix(plan.LinuxArtifacts[2].Contents, "\n")
	widened := strings.Replace(line, "restrict,", "restrict,pty,", 1)
	path := filepath.Join(sshDirectory, "authorized_keys")
	if err := os.WriteFile(path, []byte(widened+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := appendSimpleAuthorizedKeyAt(path, plan.LinuxArtifacts[2].Contents, backups); !errors.Is(err, ErrSimpleArtifactConflict) {
		t.Fatalf("widened matching key error=%v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "elsewhere")
	if err := os.WriteFile(target, []byte("must not be followed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if err := appendSimpleAuthorizedKeyAt(path, plan.LinuxArtifacts[2].Contents, backups); !errors.Is(err, ErrSimpleArtifactConflict) {
		t.Fatalf("symlink authorized_keys error=%v", err)
	}
}
