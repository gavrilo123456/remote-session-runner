package laneinstall

import (
	"encoding/xml"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"remote-session-runner/src/internal/config"
)

func TestPRDCatalogServiceArtifactsSelectCatalogBinariesWithoutMovingState(t *testing.T) {
	macArtifacts, err := BuildPRDCatalogServiceArtifacts("mac-control-plane")
	if err != nil || len(macArtifacts) != 2 {
		t.Fatalf("Mac PRD catalog artifacts=%+v err=%v", macArtifacts, err)
	}
	macPlan, _ := BuildCatalogPlan("mac-control-plane", config.ReleaseLanePRD)
	for _, artifact := range macArtifacts {
		checkCatalogPlistXML(t, artifact.Contents)
		if !strings.Contains(artifact.Contents, "current/bin/runner-log-capture") || !strings.Contains(artifact.Contents, macPlan.Namespace.ConfigFile) ||
			strings.Contains(artifact.Contents, "runner-releases") || strings.Contains(artifact.Contents, "projects-runtime") {
			t.Errorf("Mac PRD launch artifact does not preserve config and select current binaries: %+v", artifact)
		}
	}
	if !strings.Contains(macArtifacts[0].Contents, "current/bin/runner-local") || !strings.Contains(macArtifacts[1].Contents, "current/bin/runner-locald") {
		t.Fatalf("Mac PRD launch artifacts do not select both current services: %+v", macArtifacts)
	}

	for _, host := range []string{"linux-host", "sandbox-host", "factory-sandbox"} {
		artifacts, err := BuildPRDCatalogServiceArtifacts(host)
		if err != nil || len(artifacts) != 3 {
			t.Fatalf("%s PRD catalog artifacts=%+v err=%v", host, artifacts, err)
		}
		plan, _ := BuildCatalogPlan(host, config.ReleaseLanePRD)
		if artifacts[0].Kind != ArtifactEntrypoint || !strings.Contains(artifacts[0].Contents, "selected_host='"+host+"'") ||
			!strings.Contains(artifacts[0].Contents, "exec \"$service_root/current/bin/runnerd\"") ||
			!strings.Contains(artifacts[0].Contents, "$service_root/config/linux.yaml") || strings.Contains(artifacts[0].Contents, "/projects-runtime/runner-releases/") {
			t.Errorf("%s PRD entrypoint is not bound to the host catalog selector or moved config into the catalog", host)
		}
		if artifacts[1].Kind != ArtifactSystemdUnit || !strings.Contains(artifacts[1].Contents, "current/bin/runner-log-capture") ||
			!strings.Contains(artifacts[1].Contents, plan.Namespace.RunnerEntrypoint) {
			t.Errorf("%s PRD systemd unit does not preserve service identity and select catalog log capture: %+v", host, artifacts[1])
		}
		if artifacts[2].Kind != ArtifactSSHWrapper || !strings.Contains(artifacts[2].Contents, "selected_host='"+host+"'") ||
			!strings.Contains(artifacts[2].Contents, "--catalog-host \"$selected_host\" --catalog-lane prd") ||
			!strings.Contains(artifacts[2].Contents, plan.Namespace.PrivateSocket) {
			t.Errorf("%s PRD queued wrapper is not bound to its catalog and existing socket: %+v", host, artifacts[2])
		}
		checkCatalogShellSyntax(t, artifacts[0].Contents)
		checkCatalogShellSyntax(t, artifacts[2].Contents)
		if plan.Namespace.Database != "/home/ubuntu/.local/share/remote-session-runner/state/remote.db" || plan.Namespace.PrivateSocket != "/home/ubuntu/.local/share/remote-session-runner/run/runnerd.sock" {
			t.Errorf("%s catalog plan moved existing writable paths: %+v", host, plan.Namespace)
		}
	}
}

func checkCatalogPlistXML(t *testing.T, contents string) {
	t.Helper()
	decoder := xml.NewDecoder(strings.NewReader(contents))
	var root xml.Name
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Mac PRD LaunchAgent is not valid XML: %v", err)
		}
		if start, ok := token.(xml.StartElement); ok && root.Local == "" {
			root = start.Name
		}
	}
	if root.Local != "plist" {
		t.Fatalf("Mac PRD LaunchAgent XML root=%q, want plist", root.Local)
	}
}

func checkCatalogShellSyntax(t *testing.T, contents string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "generated.sh")
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("/bin/sh", "-n", path).CombinedOutput(); err != nil {
		t.Fatalf("generated shell artifact has invalid syntax: %v: %s", err, output)
	}
}
