package laneinstall

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"remote-session-runner/src/internal/config"
)

func TestSimplePlanUsesOnlyExistingHostLanePathsAndServices(t *testing.T) {
	hosts := []string{"mac-control-plane", "linux-host", "sandbox-host", "factory-sandbox"}
	for _, host := range hosts {
		for _, lane := range []config.ReleaseLane{config.ReleaseLaneDev, config.ReleaseLaneQAS} {
			t.Run(host+"/"+string(lane), func(t *testing.T) {
				plan, err := BuildSimplePlan(host, lane)
				if err != nil {
					t.Fatal(err)
				}
				if plan.HostID != host || plan.Lane != lane || plan.Namespace.Lane != lane || len(plan.Artifacts) == 0 {
					t.Fatalf("simple plan identity/artifacts = %+v", plan)
				}
				want, err := config.DeriveSimpleLaneNamespace(plan.Namespace.Host, lane, plan.Namespace.RemoteProfile)
				if err != nil {
					t.Fatal(err)
				}
				if plan.Namespace != want {
					t.Fatalf("plan selected a non-simple namespace: got=%+v want=%+v", plan.Namespace, want)
				}
				if !strings.HasSuffix(plan.Namespace.ServiceRoot, "-"+string(lane)) ||
					(host != "mac-control-plane" && plan.Namespace.QueuePort != 22) {
					t.Fatalf("lane root or existing SSH port is wrong: %+v", plan.Namespace)
				}
				for _, artifact := range plan.Artifacts {
					if artifact.InstallPath == "" || artifact.Contents == "" ||
						strings.Contains(artifact.InstallPath, "RemoteSessionRunner/") ||
						strings.Contains(artifact.Contents, "RemoteSessionRunner/") {
						t.Fatalf("incomplete artifact or unsuffixed PRD reference: %+v", artifact)
					}
					for _, forbidden := range []string{"runner-sshd", "2224", "2225", "Slice=", ".slice", "/etc/remote-session-runner/ssh", ":9443"} {
						if strings.Contains(artifact.InstallPath, forbidden) || strings.Contains(artifact.Contents, forbidden) {
							t.Fatalf("simple artifact contains superseded infrastructure %q: %+v", forbidden, artifact)
						}
					}
				}
				if host == "mac-control-plane" {
					if len(plan.Artifacts) != 2 || plan.Namespace.LocalAgentLabel != "com.remote-session-runner.local-"+string(lane) ||
						plan.Artifacts[0].InstallPath != plan.Namespace.LocalAgentPlist || plan.Artifacts[1].InstallPath != plan.Namespace.LocalDAgentPlist {
						t.Fatalf("Mac lane agents do not match the fixed namespace: %+v", plan)
					}
					return
				}
				if len(plan.Artifacts) != 2 || plan.Namespace.RunnerUnit != "runnerd-"+string(lane)+".service" ||
					plan.Artifacts[0].InstallPath != plan.Namespace.RunnerEntrypoint ||
					plan.Artifacts[1].InstallPath != filepath.Join("/etc/systemd/system", plan.Namespace.RunnerUnit) {
					t.Fatalf("Linux lane service artifacts do not match the fixed namespace: %+v", plan)
				}
				service := plan.Artifacts[1].Contents
				for _, required := range []string{"User=ubuntu", "Group=ubuntu", "WorkingDirectory=" + plan.Namespace.ServiceRoot, filepath.Join(plan.Namespace.ServiceRoot, "current", "bin", "runner-log-capture"), "TimeoutStopSec=30s", "WantedBy=multi-user.target"} {
					if !strings.Contains(service, required) {
						t.Errorf("Linux service is missing %q", required)
					}
				}
				if strings.Contains(service, "Slice=") || strings.Contains(service, "runner-sshd") {
					t.Fatalf("Linux service adds a cgroup slice or SSH daemon: %s", service)
				}
				if host != "mac-control-plane" {
					unit := plan.Artifacts[1].Contents
					if !strings.Contains(unit, "ExecStart=/home/ubuntu/.local/share/remote-session-runner-") ||
						strings.Contains(unit, "ExecStart='") || strings.Contains(unit, "Slice=") ||
						!strings.Contains(unit, "--active-file-bytes 16777216 --rotated-file-bytes 16777216 --max-rotated-files 4 --temp-file-bytes 16777216") {
						t.Fatalf("Linux service command is not a fixed direct ExecStart: %s", unit)
					}
				} else {
					for _, artifact := range plan.Artifacts {
						if !strings.Contains(artifact.Contents, "<string>--active-file-bytes</string>") ||
							!strings.Contains(artifact.Contents, "<string>16777216</string>") ||
							!strings.Contains(artifact.Contents, "<string>--max-rotated-files</string>") ||
							!strings.Contains(artifact.Contents, "<string>4</string>") {
							t.Fatalf("Mac lane LaunchAgent omits its bounded log policy: %+v", artifact)
						}
					}
				}
			})
		}
	}
}

func TestSimplePlanRejectsPRDAndUnknownHosts(t *testing.T) {
	if _, err := BuildSimplePlan("linux-host", config.ReleaseLanePRD); !errors.Is(err, ErrInvalidLane) {
		t.Fatalf("PRD plan error=%v, want %v", err, ErrInvalidLane)
	}
	for _, host := range []string{"", "ubuntu", "linux-host-dev", "unknown-host"} {
		if _, err := BuildSimplePlan(host, config.ReleaseLaneDev); !errors.Is(err, ErrInvalidHost) {
			t.Errorf("BuildSimplePlan(%q) error=%v, want %v", host, err, ErrInvalidHost)
		}
	}
}

func TestGeneratedLinuxEntrypointAcceptsOnlyCanonicalLegacyOrCatalogSelectors(t *testing.T) {
	for _, host := range []string{"linux-host", "sandbox-host", "factory-sandbox"} {
		plan, err := BuildSimplePlan(host, config.ReleaseLaneDev)
		if err != nil {
			t.Fatal(err)
		}
		entrypoint := plan.Artifacts[0].Contents
		for _, required := range []string{
			"catalog_root='/home/ubuntu/projects-runtime/runner-releases'",
			"selected_host='" + host + "'",
			"releases/*)",
			"\"$catalog_root/\"*)",
			"host_suffix=",
			"$catalog_root/$release_id/$selected_host",
			"check_sealed_directory",
			"case \"$release_id\" in",
			"dev|qas|prd|latest|current",
			"runnerd: lane service path is not ready",
		} {
			if !strings.Contains(entrypoint, required) {
				t.Errorf("%s entrypoint is missing catalog selector guard %q", host, required)
			}
		}
		if strings.Contains(entrypoint, "eval ") || strings.Contains(entrypoint, "source ") {
			t.Errorf("%s entrypoint evaluates selector-controlled text: %s", host, entrypoint)
		}
	}
}
