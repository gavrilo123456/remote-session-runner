package config

import (
	"fmt"
	"path/filepath"

	"remote-session-runner/src/internal/domain"
)

// validateV3RuntimeDocument validates and assembles the complete active
// existing-hosts runtime configuration. The V3 identity and profile parser
// remains the single authority for lane paths, endpoints, credentials and
// controller bindings; this function joins it to the runtime policy registry.
func validateV3RuntimeDocument(document fileDocument) (Config, error) {
	if !validReleaseLane(document.ReleaseLane) || document.LaneProfiles == nil ||
		(document.Mac == nil) == (document.Linux == nil) || document.RemoteHosts != nil {
		return Config{}, ErrInvalidConfig
	}

	host := HostKindMac
	if document.Linux != nil {
		host = HostKindLinux
	}
	bindings, err := v3RuntimeProfileBindings(document, host)
	if err != nil {
		return Config{}, err
	}
	validated, err := ValidateSimpleV3ProfileBindings(bindings)
	if err != nil {
		return Config{}, ErrInvalidLaneProfiles
	}

	defaultsBase := domain.DefaultServiceLimits()
	if document.ReleaseLane != ReleaseLanePRD {
		defaultsBase.ActiveSessionsPerHost = 2
		defaultsBase.RunningCommandsPerHost = 1
	}
	defaults, retention, err := validateLimitsAndRetentionWithDefaults(document, defaultsBase)
	if err != nil {
		return Config{}, err
	}
	if document.ReleaseLane != ReleaseLanePRD && !withinSimpleLaneCeiling(defaults) {
		return Config{}, ErrInvalidConfig
	}

	config := Config{
		schemaVersion:     VersionV3,
		releaseLane:       document.ReleaseLane,
		laneProfiles:      &validated,
		defaults:          defaults,
		retention:         retention,
		environments:      make(map[string]RegisteredEnvironment, len(document.EnvironmentRegistry)),
		secretRefs:        make(map[SecretName]SecretReference),
		executionContexts: make(map[string]ExecutionContext),
		remoteHosts:       make(map[string]RemoteHostProfile),
		directEndpoints:   make(map[string]DirectEndpointProfile),
		mailboxes:         make(map[string]MailboxDefinition),
		defaultSource:     domain.SourceModeEmpty,
	}
	if err := populateEnvironments(&config, document.EnvironmentRegistry); err != nil {
		return Config{}, err
	}
	if document.ReleaseLane != ReleaseLanePRD {
		for _, environment := range config.environments {
			if !withinSimpleLaneCeiling(environment.policy.ServiceLimits()) {
				return Config{}, ErrInvalidConfig
			}
		}
	}

	if host == HostKindMac {
		if err := validateV3MacRuntimeDocument(document.Mac, document.SecretReferences, document, validated); err != nil {
			return Config{}, err
		}
		config.kind = HostKindMac
		settings := macSettings(document.Mac)
		config.mac = &settings
		if err := populateV3MacRuntimeRegistries(&config, document); err != nil {
			return Config{}, err
		}
		return config, nil
	}

	if err := validateV3LinuxRuntimeDocument(document.Linux, document.SecretReferences, document, validated); err != nil {
		return Config{}, err
	}
	config.kind = HostKindLinux
	settings := linuxSettings(document.Linux)
	config.linux = &settings
	profile, ok := validated.LinuxProfileBinding()
	if !ok {
		return Config{}, ErrInvalidLaneProfiles
	}
	config.secretRefs[SecretLinuxServerTLSKey] = SecretReference{File: profile.Direct.ServerPrivateKeyFile}
	if err := populateV3LinuxContexts(&config, document); err != nil {
		return Config{}, err
	}
	return config, nil
}

func v3RuntimeProfileBindings(document fileDocument, host HostKind) (V3ProfileBindings, error) {
	if document.LaneProfiles == nil || len(document.EnvironmentRegistry) == 0 || len(document.ExecutionContexts) == 0 {
		return V3ProfileBindings{}, ErrInvalidConfig
	}
	bindings := V3ProfileBindings{
		Version:       VersionV3,
		ReleaseLane:   document.ReleaseLane,
		Host:          host,
		RemoteProfile: document.LaneProfiles.RemoteProfile,
		MacProfiles:   append([]V3MacRemoteProfile(nil), document.LaneProfiles.MacProfiles...),
		LinuxProfile:  document.LaneProfiles.LinuxProfile,
	}
	if document.LaneProfiles.Host != host {
		return V3ProfileBindings{}, ErrInvalidConfig
	}
	for name, environment := range document.EnvironmentRegistry {
		binding := V3EnvironmentBinding{Name: name, EffectiveAccount: environment.EffectiveAccount}
		for _, target := range environment.AllowedTargets {
			binding.AllowedTargets = append(binding.AllowedTargets, V3TargetBinding{Kind: target.Kind, Profile: target.Profile})
		}
		for _, controller := range environment.AllowedControllers {
			binding.AllowedControllers = append(binding.AllowedControllers, V3ControllerBinding{Type: string(controller.Type), ID: controller.ID})
		}
		bindings.Environments = append(bindings.Environments, binding)
	}
	for name, context := range document.ExecutionContexts {
		bindings.ExecutionContexts = append(bindings.ExecutionContexts, V3ExecutionContextBinding{
			Name: name, Environment: context.Environment,
			Target: V3TargetBinding{Kind: context.ExecutionTarget.Kind, Profile: context.ExecutionTarget.Profile},
		})
	}
	return bindings, nil
}

func validateV3MacRuntimeDocument(mac *macDocument, refs secretReferencesDocument, document fileDocument, profiles ValidatedV3Profiles) error {
	namespace, err := DeriveSimpleLaneNamespace(HostKindMac, document.ReleaseLane, "")
	if err != nil || mac == nil || mac.Account != namespace.Account || mac.ServiceRoot != namespace.ServiceRoot ||
		!validServiceRoot(mac.ServiceRoot) || mac.MailboxRoot != namespace.MailboxRoot ||
		(mac.ReconciliationDeadline != nil && mac.ReconciliationDeadline.Duration() <= 0) {
		return ErrInvalidConfig
	}
	if mac.RemoteEndpointProfile != "" || mac.RemoteEndpoint != "" || mac.RemoteServerCA != "" ||
		mac.SSHHostAlias != "" || mac.SSHKnownHosts != "" || mac.DirectClientCertificate != "" {
		return ErrInvalidConfig
	}
	if !selectedPaths(mac.ServiceRoot,
		servicePath{mac.APISocket, "run/local-api.sock"},
		servicePath{mac.LocalDSocket, "run/locald.sock"},
		servicePath{mac.Database, "state/local.db"},
		servicePath{mac.MailboxRoot, "mailbox"},
		servicePath{mac.Workspaces, "workspaces"},
		servicePath{mac.ScriptTempRoot, "tmp/scripts"},
		servicePath{mac.Backups, "backups"},
	) {
		return ErrInvalidConfig
	}
	if refs.DispatcherSSHKey != nil || refs.DirectClientKey != nil || refs.LinuxServerKey != nil {
		return ErrInvalidSecretRef
	}
	if len(document.Mailboxes) == 0 || len(document.RemoteHosts) != 0 || document.Linux != nil {
		return ErrInvalidConfig
	}
	if _, ok := profiles.MacProfile("linux-host"); !ok {
		return ErrInvalidLaneProfiles
	}
	return nil
}

func populateV3MacRuntimeRegistries(config *Config, document fileDocument) error {
	if _, ok := document.Mailboxes["default"]; !ok {
		return ErrInvalidConfig
	}
	for _, profile := range document.LaneProfiles.MacProfiles {
		queued := QueuedBridgeProfile{
			Host: profile.Queued.Host, Port: profile.Queued.Port, KnownHostsFile: profile.Queued.KnownHostsFile,
			PrivateKeyFile:                 profile.Queued.DispatcherPrivateKeyFile,
			HostKeyFingerprint:             profile.Queued.HostKeyFingerprint,
			DispatcherPublicKeyFingerprint: profile.Queued.DispatcherPublicKeyFingerprint,
		}
		direct := DirectEndpointProfile{
			Name: profile.Direct.Name, TargetProfile: profile.Profile, Endpoint: profile.Direct.URL,
			ServerCA: profile.Direct.ServerCAFile, ClientCertificate: profile.Direct.ClientCertificateFile,
			ClientPrivateKeyFile:        profile.Direct.ClientPrivateKeyFile,
			ServerCAFingerprint:         profile.Direct.ServerCAFingerprint,
			ClientCertFingerprint:       profile.Direct.ClientCertificateFingerprint,
			ClientSPKIFingerprint:       profile.Direct.ClientSPKIFingerprint,
			ExpectedServerFingerprint:   profile.Direct.ExpectedServerCertificateFingerprint,
			ExpectedClientCAFingerprint: profile.Direct.ExpectedClientCAFingerprint,
			ControllerType:              profile.Direct.ControllerType, ControllerID: profile.Direct.ControllerID,
			ClientURISAN: profile.Direct.ClientURISAN, ServerURISAN: profile.Direct.ServerURISAN,
		}
		config.remoteHosts[profile.Profile] = RemoteHostProfile{
			Profile: profile.Profile, Account: LinuxAccount, QueuedBridge: &queued, DirectEndpoint: &direct,
		}
		if _, duplicate := config.directEndpoints[direct.Name]; duplicate {
			return ErrInvalidConfig
		}
		config.directEndpoints[direct.Name] = direct
	}
	if err := validateV2MacEnvironmentAccounts(document.EnvironmentRegistry, config.remoteHosts); err != nil {
		return err
	}
	for name, definition := range document.ExecutionContexts {
		context, err := validateExecutionContext(name, definition, document.EnvironmentRegistry, config.remoteHosts)
		if err != nil {
			return err
		}
		config.executionContexts[name] = context
	}
	roots := make([]string, 0, len(document.Mailboxes))
	for id, definition := range document.Mailboxes {
		mailbox, err := validateMailboxDefinition(config.mac.ServiceRoot, id, definition, config.executionContexts, config.remoteHosts)
		if err != nil || !pathUnder(config.mac.ServiceRoot, mailbox.Root) {
			return ErrInvalidConfig
		}
		expected := filepath.Join(config.mac.ServiceRoot, "mailboxes", id)
		if id == "default" {
			expected = filepath.Join(config.mac.ServiceRoot, "mailbox")
		}
		if mailbox.Root != expected {
			return ErrInvalidConfig
		}
		for _, root := range roots {
			if sameOrNestedPath(root, mailbox.Root) {
				return ErrInvalidConfig
			}
		}
		roots = append(roots, mailbox.Root)
		config.mailboxes[id] = mailbox
	}
	return nil
}

func validateV3LinuxRuntimeDocument(linux *linuxDocument, refs secretReferencesDocument, document fileDocument, profiles ValidatedV3Profiles) error {
	if linux == nil || linux.RuntimeAdapter != "linux-host-process" || linux.TLSMinVersion != "1.3" {
		return fmt.Errorf("%w: %w", ErrInvalidConfig, ErrConfigHostProfile)
	}
	profile, ok := profiles.LinuxProfileBinding()
	if !ok || linux.RemoteTargetProfile != profile.RemoteProfile {
		return ErrInvalidLaneProfiles
	}
	namespace, err := DeriveSimpleLaneNamespace(HostKindLinux, document.ReleaseLane, profile.RemoteProfile)
	if err != nil || linux.Account != namespace.Account || linux.ServiceRoot != namespace.ServiceRoot || !validServiceRoot(linux.ServiceRoot) ||
		linux.DirectHTTPSBind != namespace.DirectHTTPSBind || linux.DirectPublicEndpoint != namespace.DirectEndpointURL ||
		linux.ServerCertificate != profile.Direct.ServerCertificateFile || linux.ClientCA != profile.Direct.ClientCAFile ||
		linux.ClientPrincipalMap != profile.Direct.ClientPrincipalMapFile {
		return ErrInvalidConfig
	}
	if !selectedPaths(linux.ServiceRoot,
		servicePath{linux.Database, "state/remote.db"},
		servicePath{linux.PrivateSocket, "run/runnerd.sock"},
		servicePath{linux.Workspaces, "workspaces"},
		servicePath{linux.ScriptTempRoot, "tmp/scripts"},
		servicePath{linux.Backups, "backups"},
		servicePath{linux.ServerCertificate, filepath.ToSlash(filepath.Join("secrets", "profiles", profile.RemoteProfile, "tls", "server.pem"))},
		servicePath{linux.ClientCA, filepath.ToSlash(filepath.Join("secrets", "profiles", profile.RemoteProfile, "tls", "client-ca.pem"))},
		servicePath{linux.ClientPrincipalMap, filepath.ToSlash(filepath.Join("config", "client-principals.yaml"))},
	) {
		return ErrInvalidConfig
	}
	if (refs.LinuxServerKey != nil && refs.LinuxServerKey.File != profile.Direct.ServerPrivateKeyFile) ||
		refs.DispatcherSSHKey != nil || refs.DirectClientKey != nil {
		return ErrInvalidSecretRef
	}
	if document.Mailboxes != nil || document.RemoteHosts != nil || document.Mac != nil {
		return ErrInvalidConfig
	}
	for _, environment := range document.EnvironmentRegistry {
		if environment.EffectiveAccount != LinuxAccount || !sameTargets(environment.AllowedTargets,
			targetDocument{Kind: domain.TargetKindRemote, Profile: profile.RemoteProfile}) {
			return ErrInvalidConfig
		}
	}
	return nil
}

func populateV3LinuxContexts(config *Config, document fileDocument) error {
	profile, ok := config.laneProfiles.LinuxProfileBinding()
	if !ok {
		return ErrInvalidLaneProfiles
	}
	for name, definition := range document.ExecutionContexts {
		context, err := validateExecutionContext(name, definition, document.EnvironmentRegistry, map[string]RemoteHostProfile{
			profile.RemoteProfile: {Profile: profile.RemoteProfile, Account: LinuxAccount,
				QueuedBridge: &QueuedBridgeProfile{Host: profile.Queued.Host, Port: profile.Queued.Port}},
		})
		if err != nil {
			return err
		}
		config.executionContexts[name] = context
	}
	return nil
}

func withinSimpleLaneCeiling(limits domain.ServiceLimits) bool {
	return limits.ActiveSessionsPerHost <= 2 && limits.RunningCommandsPerHost <= 1
}
