package laneinstall

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"remote-session-runner/src/internal/config"
)

func TestLanesL205CaptureWiringIsCandidateAndNullInEveryLane(t *testing.T) {
	makefile, err := os.ReadFile(filepath.Join("..", "..", "..", "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(makefile), "runner-log-capture") {
		t.Fatal("runner-log-capture is absent from the host build/smoke candidate set")
	}
	for _, host := range []string{"mac-control-plane", "linux-host", "sandbox-host", "factory-sandbox"} {
		for _, lane := range []config.ReleaseLane{config.ReleaseLaneDev, config.ReleaseLaneQAS} {
			plan, err := BuildPlan(host, lane)
			if err != nil {
				t.Fatal(err)
			}
			if filepath.Base(plan.Namespace.LogCapture) != "runner-log-capture" {
				t.Fatalf("%s/%s log-capture component path=%q", host, lane, plan.Namespace.LogCapture)
			}
			if host == "mac-control-plane" {
				for _, artifact := range plan.Artifacts {
					if artifact.Kind != ArtifactLaunchAgent || !strings.Contains(artifact.Contents, plan.Namespace.LogCapture) ||
						!strings.Contains(artifact.Contents, "<string>--log-path</string>") || !strings.Contains(artifact.Contents, "<string>/dev/null</string>") {
						t.Fatalf("Mac %s LaunchAgent does not wrap and null direct streams: %+v", lane, artifact)
					}
				}
				continue
			}
			var service, entrypoint string
			for _, artifact := range plan.Artifacts {
				switch artifact.Kind {
				case ArtifactSystemdUnit:
					if strings.HasSuffix(artifact.InstallPath, ".service") {
						service = artifact.Contents
					}
				case ArtifactEntrypoint:
					entrypoint = artifact.Contents
				}
			}
			if !strings.Contains(service, plan.Namespace.LogCapture+" --log-path ") ||
				!strings.Contains(service, "StandardOutput=null") || !strings.Contains(service, "StandardError=null") ||
				!strings.Contains(entrypoint, "check_file \"$service_root/bin/runner-log-capture\" 700") {
				t.Fatalf("Linux %s/%s service does not wrap, attest-path-check, and null streams", host, lane)
			}
		}
	}
}
