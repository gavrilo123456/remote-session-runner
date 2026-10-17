// Package laneinstall describes the fixed Dev/QAS service installation
// artifacts. It renders plans and templates only; it never installs services
// or creates lane roots.
package laneinstall

import (
	"errors"
	"fmt"
	"path/filepath"

	"remote-session-runner/src/internal/config"
)

var (
	ErrInvalidHost = errors.New("invalid canonical lane host")
	ErrInvalidLane = errors.New("lane installation accepts only dev or qas")
)

type ArtifactKind string

const (
	ArtifactLaunchAgent   ArtifactKind = "launch_agent_plist"
	ArtifactSystemdUnit   ArtifactKind = "systemd_unit"
	ArtifactEntrypoint    ArtifactKind = "lane_entrypoint"
	ArtifactSSHConfig     ArtifactKind = "queued_ssh_config"
	ArtifactSSHKeys       ArtifactKind = "queued_ssh_authorized_keys"
	ArtifactSSHWrapper    ArtifactKind = "queued_ssh_forced_wrapper"
	ArtifactSSHUnit       ArtifactKind = "queued_ssh_systemd_unit"
	ArtifactSSHKnownHosts ArtifactKind = "mac_queued_ssh_known_hosts"
	ArtifactSystemdDropIn ArtifactKind = "systemd_dropin"
	ArtifactProjectMap    ArtifactKind = "project_map_fragment"
	ArtifactProjectQuota  ArtifactKind = "project_quota_fragment"
)

// Artifact is a deterministic file produced by a fixed plan. InstallPath is
// the only supported destination; callers cannot supply a root or override a
// service identity. Rendering does not write InstallPath.
type Artifact struct {
	Kind        ArtifactKind
	InstallPath string
	Mode        uint32
	Owner       string
	Group       string
	Contents    string
}

type Plan struct {
	HostID    string
	Lane      config.ReleaseLane
	Namespace config.LaneNamespace
	Artifacts []Artifact
}

type hostTuple struct {
	kind    config.HostKind
	profile string
}

var canonicalHosts = map[string]hostTuple{
	"mac-control-plane": {kind: config.HostKindMac},
	"linux-host":        {kind: config.HostKindLinux, profile: "linux-host"},
	"sandbox-host":      {kind: config.HostKindLinux, profile: "sandbox-host"},
	"factory-sandbox":   {kind: config.HostKindLinux, profile: "factory-sandbox"},
}

// BuildPlan derives the only allowed Dev or QAS namespace for one canonical
// host and renders its fixed service artifacts. PRD is deliberately excluded:
// the existing unsuffixed production installation is not an L2 target.
func BuildPlan(hostID string, lane config.ReleaseLane) (Plan, error) {
	if lane != config.ReleaseLaneDev && lane != config.ReleaseLaneQAS {
		return Plan{}, ErrInvalidLane
	}
	tuple, ok := canonicalHosts[hostID]
	if !ok {
		return Plan{}, ErrInvalidHost
	}
	ns, err := config.DeriveLaneNamespace(tuple.kind, lane, tuple.profile)
	if err != nil {
		return Plan{}, fmt.Errorf("derive fixed namespace: %w", err)
	}
	plan := Plan{HostID: hostID, Lane: lane, Namespace: ns}
	if tuple.kind == config.HostKindMac {
		plan.Artifacts = macArtifacts(ns)
	} else {
		plan.Artifacts = linuxArtifacts(ns)
	}
	return plan, nil
}

func macArtifacts(ns config.LaneNamespace) []Artifact {
	return []Artifact{
		{Kind: ArtifactLaunchAgent, InstallPath: ns.LocalAgentPlist, Mode: 0o644, Contents: renderLaunchAgent(ns.LocalAgentLabel, ns.ServiceRoot, ns.LogCapture, filepath.Join(ns.Logs, "local.log"), ns.LocalBinary, ns.ConfigFile)},
		{Kind: ArtifactLaunchAgent, InstallPath: ns.LocalDAgentPlist, Mode: 0o644, Contents: renderLaunchAgent(ns.LocalDAgentLabel, ns.ServiceRoot, ns.LogCapture, filepath.Join(ns.Logs, "locald.log"), ns.LocalDBinary, ns.ConfigFile)},
	}
}

func linuxArtifacts(ns config.LaneNamespace) []Artifact {
	entrypoint := filepath.Join(ns.Binaries, "runnerd-entrypoint.sh")
	service := renderSystemdService(ns)
	slice := renderSystemdSlice(ns)
	return []Artifact{
		{Kind: ArtifactEntrypoint, InstallPath: entrypoint, Mode: 0o700, Contents: renderLinuxEntrypoint(ns)},
		{Kind: ArtifactSystemdUnit, InstallPath: filepath.Join("/etc/systemd/system", ns.RunnerUnit), Mode: 0o644, Contents: service},
		{Kind: ArtifactSystemdUnit, InstallPath: filepath.Join("/etc/systemd/system", ns.RunnerSlice), Mode: 0o644, Contents: slice},
	}
}
