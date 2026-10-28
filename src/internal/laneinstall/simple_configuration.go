package laneinstall

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"remote-session-runner/src/internal/config"
)

// InstallConfiguration accepts only a complete V3 configuration that already
// binds the fixed host and selected lane. Secret values remain path-only and
// are never opened. Existing different config is preserved and rejected.
func (m *SimpleLaneMaintenance) InstallConfiguration(ctx context.Context, contents []byte) error {
	if m == nil || m.closed || !m.fresh || validateSimplePlan(m.plan) != nil ||
		len(contents) == 0 || len(contents) > config.MaxConfigBytes {
		return ErrSimpleLanePreflight
	}
	if err := m.PrepareLayout(ctx); err != nil {
		return err
	}
	parent := filepath.Dir(m.plan.Namespace.ConfigFile)
	candidate, err := os.CreateTemp(parent, ".lane-config-candidate-*")
	if err != nil {
		return err
	}
	candidatePath := candidate.Name()
	defer os.Remove(candidatePath)
	if err := candidate.Chmod(0o600); err != nil {
		_ = candidate.Close()
		return err
	}
	if _, err := candidate.Write(contents); err != nil {
		_ = candidate.Close()
		return err
	}
	if err := candidate.Sync(); err != nil {
		_ = candidate.Close()
		return err
	}
	if err := candidate.Close(); err != nil {
		return err
	}
	loaded, err := config.LoadFile(candidatePath)
	if err != nil || !simpleLoadedConfigMatches(m.plan, loaded) {
		return ErrSimpleLanePreflight
	}
	artifact := Artifact{InstallPath: m.plan.Namespace.ConfigFile, Mode: 0o600, Contents: string(contents)}
	if err := writeSimpleUserArtifact(artifact); err != nil {
		if errors.Is(err, ErrSimpleArtifactConflict) {
			return fmt.Errorf("preserve existing selected-lane config: %w", err)
		}
		return err
	}
	return nil
}

func simpleLoadedConfigMatches(plan Plan, loaded config.Config) bool {
	if loaded.ReleaseLane() != plan.Lane || loaded.SchemaVersion() != config.VersionV3 || loaded.Kind() != plan.Namespace.Host {
		return false
	}
	profiles, ok := loaded.LaneProfiles()
	if !ok || profiles.Lane() != plan.Lane || profiles.Host() != plan.Namespace.Host ||
		(plan.Namespace.Host == config.HostKindLinux && profiles.RemoteProfile() != plan.Namespace.RemoteProfile) {
		return false
	}
	if plan.Namespace.Host == config.HostKindMac {
		settings, ok := loaded.MacSettings()
		if !ok || settings.Account != plan.Namespace.Account || settings.ServiceRoot != plan.Namespace.ServiceRoot ||
			settings.APISocket != plan.Namespace.APISocket || settings.LocalDSocket != plan.Namespace.LocalDSocket ||
			settings.Database != plan.Namespace.Database || settings.MailboxRoot != plan.Namespace.MailboxRoot ||
			settings.Workspaces != plan.Namespace.Workspaces || settings.ScriptTempRoot != plan.Namespace.ScriptTempRoot ||
			settings.Backups != plan.Namespace.Backups {
			return false
		}
		mailbox, ok := loaded.Mailbox("default")
		return ok && mailbox.Root == plan.Namespace.MailboxRoot
	}
	settings, ok := loaded.LinuxSettings()
	linuxProfile, hasLinuxProfile := profiles.LinuxProfileBinding()
	return ok && hasLinuxProfile && settings.Account == plan.Namespace.Account && settings.ServiceRoot == plan.Namespace.ServiceRoot &&
		settings.Database == plan.Namespace.Database && settings.PrivateSocket == plan.Namespace.PrivateSocket &&
		settings.Workspaces == plan.Namespace.Workspaces && settings.ScriptTempRoot == plan.Namespace.ScriptTempRoot &&
		settings.Backups == plan.Namespace.Backups && settings.DirectHTTPSBind == linuxProfile.Direct.Bind &&
		settings.DirectPublicEndpoint == linuxProfile.Direct.URL && settings.RuntimeAdapter == "linux-host-process" &&
		settings.TLSMinVersion == "1.3"
}
