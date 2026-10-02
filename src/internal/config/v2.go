package config

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/text/unicode/norm"

	"remote-session-runner/src/internal/domain"
)

// validateV2Document accepts the deliberately small registry shape used by
// the multi-inbox PoC. Version-one validation remains intentionally stricter
// and is kept in config.go for compatibility with the installed deployment.
func validateV2Document(document fileDocument) (Config, error) {
	if (document.Mac == nil) == (document.Linux == nil) {
		return Config{}, ErrInvalidConfig
	}
	defaults, retention, err := validateLimitsAndRetention(document)
	if err != nil {
		return Config{}, err
	}

	config := Config{
		schemaVersion:     VersionV2,
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

	if document.Mac != nil {
		if err := validateV2MacDocument(document.Mac, document.SecretReferences); err != nil {
			return Config{}, err
		}
		config.kind = HostKindMac
		settings := macSettings(document.Mac)
		config.mac = &settings
		if err := populateEnvironments(&config, document.EnvironmentRegistry); err != nil {
			return Config{}, err
		}
		if err := populateV2MacRegistries(&config, document); err != nil {
			return Config{}, err
		}
		return config, nil
	}

	if err := validateV2LinuxDocument(document.Linux, document.SecretReferences, document); err != nil {
		return Config{}, err
	}
	config.kind = HostKindLinux
	settings := linuxSettings(document.Linux)
	config.linux = &settings
	config.secretRefs[SecretLinuxServerTLSKey] = SecretReference{File: document.SecretReferences.LinuxServerKey.File}
	if err := populateEnvironments(&config, document.EnvironmentRegistry); err != nil {
		return Config{}, err
	}
	return config, nil
}

func validateV2MacDocument(mac *macDocument, refs secretReferencesDocument) error {
	if mac.Account != MacAccount || mac.ServiceRoot != MacServiceRoot || !validServiceRoot(mac.ServiceRoot) ||
		(mac.ReconciliationDeadline != nil && mac.ReconciliationDeadline.Duration() <= 0) {
		return ErrInvalidConfig
	}
	if mac.MailboxRoot != "" || mac.RemoteEndpointProfile != "" || mac.RemoteEndpoint != "" || mac.RemoteServerCA != "" ||
		mac.SSHHostAlias != "" || mac.SSHKnownHosts != "" || mac.DirectClientCertificate != "" {
		return ErrInvalidConfig
	}
	if !selectedPaths(mac.ServiceRoot,
		servicePath{mac.APISocket, "run/local-api.sock"},
		servicePath{mac.LocalDSocket, "run/locald.sock"},
		servicePath{mac.Database, "state/local.db"},
		servicePath{mac.Workspaces, "workspaces"},
		servicePath{mac.ScriptTempRoot, "tmp/scripts"},
		servicePath{mac.Backups, "backups"},
	) {
		return ErrInvalidConfig
	}
	if refs.DispatcherSSHKey != nil || refs.DirectClientKey != nil || refs.LinuxServerKey != nil {
		return ErrInvalidSecretRef
	}
	return nil
}

func validateV2LinuxDocument(linux *linuxDocument, refs secretReferencesDocument, document fileDocument) error {
	if document.ExecutionContexts != nil || document.RemoteHosts != nil || document.Mailboxes != nil {
		return ErrInvalidConfig
	}
	if linux.RuntimeAdapter != "linux-host-process" {
		return fmt.Errorf("%w: %w", ErrInvalidConfig, ErrConfigHostProfile)
	}
	if linux.Account != LinuxAccount || linux.ServiceRoot != LinuxServiceRoot || !validServiceRoot(linux.ServiceRoot) ||
		!validRegistryName(linux.RemoteTargetProfile) || !validV2Bind(linux.DirectHTTPSBind) ||
		!validV2HTTPSURL(linux.DirectPublicEndpoint) || linux.TLSMinVersion != "1.3" {
		return ErrInvalidConfig
	}
	if !selectedPaths(linux.ServiceRoot,
		servicePath{linux.Database, "state/remote.db"},
		servicePath{linux.PrivateSocket, "run/runnerd.sock"},
		servicePath{linux.Workspaces, "workspaces"},
		servicePath{linux.ScriptTempRoot, "tmp/scripts"},
		servicePath{linux.Backups, "backups"},
		servicePath{linux.ServerCertificate, "secrets/server.pem"},
		servicePath{linux.ClientCA, "secrets/client-ca.pem"},
		servicePath{linux.ClientPrincipalMap, "config/client-principals.yaml"},
	) {
		return ErrInvalidConfig
	}
	if refs.LinuxServerKey == nil || refs.DispatcherSSHKey != nil || refs.DirectClientKey != nil || !validSecretRef(linux.ServiceRoot, refs.LinuxServerKey) {
		return ErrInvalidSecretRef
	}
	if len(document.EnvironmentRegistry) == 0 {
		return ErrInvalidConfig
	}
	for _, environment := range document.EnvironmentRegistry {
		if environment.EffectiveAccount != LinuxAccount || !sameTargets(environment.AllowedTargets,
			targetDocument{Kind: domain.TargetKindRemote, Profile: linux.RemoteTargetProfile}) {
			return ErrInvalidConfig
		}
	}
	return nil
}

func populateV2MacRegistries(config *Config, document fileDocument) error {
	if len(document.ExecutionContexts) == 0 || len(document.Mailboxes) == 0 {
		return ErrInvalidConfig
	}
	if _, ok := document.Mailboxes["default"]; !ok {
		return ErrInvalidConfig
	}

	for profile, definition := range document.RemoteHosts {
		if !validRegistryName(profile) || definition.Account != LinuxAccount ||
			(definition.QueuedBridge == nil && definition.DirectEndpoint == nil) {
			return ErrInvalidConfig
		}
		host := RemoteHostProfile{Profile: profile, Account: definition.Account}
		if definition.QueuedBridge != nil {
			bridge, err := validateQueuedBridge(config.mac.ServiceRoot, *definition.QueuedBridge)
			if err != nil {
				return err
			}
			host.QueuedBridge = &bridge
		}
		if definition.DirectEndpoint != nil {
			endpoint, err := validateDirectEndpoint(config.mac.ServiceRoot, profile, *definition.DirectEndpoint)
			if err != nil {
				return err
			}
			if _, exists := config.directEndpoints[endpoint.Name]; exists {
				return ErrInvalidConfig
			}
			host.DirectEndpoint = &endpoint
			config.directEndpoints[endpoint.Name] = endpoint
		}
		config.remoteHosts[profile] = host
	}
	if err := validateV2MacEnvironmentAccounts(document.EnvironmentRegistry, config.remoteHosts); err != nil {
		return err
	}

	pairs := make(map[string]struct{}, len(document.ExecutionContexts))
	for name, definition := range document.ExecutionContexts {
		context, err := validateExecutionContext(name, definition, document.EnvironmentRegistry, config.remoteHosts)
		if err != nil {
			return err
		}
		pair := context.Environment + "\x00" + string(context.Target.Kind()) + "\x00" + context.Target.Profile()
		if _, exists := pairs[pair]; exists {
			return ErrInvalidConfig
		}
		pairs[pair] = struct{}{}
		config.executionContexts[name] = context
	}

	roots := make([]string, 0, len(document.Mailboxes))
	for id, definition := range document.Mailboxes {
		mailbox, err := validateMailboxDefinition(config.mac.ServiceRoot, id, definition, config.executionContexts, config.remoteHosts)
		if err != nil {
			return err
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

func validateV2MacEnvironmentAccounts(environments map[string]environmentDocument, hosts map[string]RemoteHostProfile) error {
	for _, environment := range environments {
		for _, target := range environment.AllowedTargets {
			switch target.Kind {
			case domain.TargetKindLocal:
				if target.Profile != "mac-workstation" || environment.EffectiveAccount != MacAccount {
					return ErrInvalidConfig
				}
			case domain.TargetKindRemote:
				if environment.EffectiveAccount != LinuxAccount {
					return ErrInvalidConfig
				}
				if _, exists := hosts[target.Profile]; !exists {
					return ErrInvalidConfig
				}
			default:
				return ErrInvalidConfig
			}
		}
	}
	return nil
}

func validateQueuedBridge(serviceRoot string, document queuedBridgeDocument) (QueuedBridgeProfile, error) {
	if !validBridgeHost(document.Host) || document.Port < 1 || document.Port > 65535 ||
		!validPathUnderSecrets(serviceRoot, document.KnownHosts) || !validPathUnderSecrets(serviceRoot, document.PrivateKey) {
		return QueuedBridgeProfile{}, ErrInvalidConfig
	}
	return QueuedBridgeProfile{
		Host: document.Host, Port: document.Port, KnownHostsFile: document.KnownHosts, PrivateKeyFile: document.PrivateKey,
	}, nil
}

func validateDirectEndpoint(serviceRoot, targetProfile string, document directEndpointDocument) (DirectEndpointProfile, error) {
	if !validRegistryName(document.Name) || !validV2HTTPSURL(document.URL) ||
		!validPathUnderSecrets(serviceRoot, document.ServerCA) ||
		!validPathUnderSecrets(serviceRoot, document.ClientCertificate) ||
		!validPathUnderSecrets(serviceRoot, document.ClientPrivateKey) {
		return DirectEndpointProfile{}, ErrInvalidConfig
	}
	return DirectEndpointProfile{
		Name: document.Name, TargetProfile: targetProfile, Endpoint: document.URL, ServerCA: document.ServerCA,
		ClientCertificate: document.ClientCertificate, ClientPrivateKeyFile: document.ClientPrivateKey,
	}, nil
}

func validateExecutionContext(name string, document executionContextDocument, environments map[string]environmentDocument, hosts map[string]RemoteHostProfile) (ExecutionContext, error) {
	if !validRegistryName(name) || !validRegistryName(document.Environment) || !validRegistryName(document.ExecutionTarget.Profile) {
		return ExecutionContext{}, ErrInvalidConfig
	}
	environment, exists := environments[document.Environment]
	if !exists || !documentAllowsTarget(environment, document.ExecutionTarget) {
		return ExecutionContext{}, ErrInvalidConfig
	}
	target, err := domain.NewExecutionTarget(document.ExecutionTarget.Kind, document.ExecutionTarget.Profile)
	if err != nil {
		return ExecutionContext{}, ErrInvalidConfig
	}
	switch target.Kind() {
	case domain.TargetKindLocal:
		if target.Profile() != "mac-workstation" || environment.EffectiveAccount != MacAccount {
			return ExecutionContext{}, ErrInvalidConfig
		}
	case domain.TargetKindRemote:
		if environment.EffectiveAccount != LinuxAccount {
			return ExecutionContext{}, ErrInvalidConfig
		}
		if _, exists := hosts[target.Profile()]; !exists {
			return ExecutionContext{}, ErrInvalidConfig
		}
	default:
		return ExecutionContext{}, ErrInvalidConfig
	}
	return ExecutionContext{Name: name, Environment: document.Environment, Target: target}, nil
}

func validateMailboxDefinition(serviceRoot, id string, document mailboxDocument, contexts map[string]ExecutionContext, hosts map[string]RemoteHostProfile) (MailboxDefinition, error) {
	if !validRegistryName(id) || !validMailboxRoot(serviceRoot, id, document.Root) || len(document.AllowedExecution) == 0 || !validRegistryName(document.DefaultExecution) {
		return MailboxDefinition{}, ErrInvalidConfig
	}
	aliases := make([]string, 0, len(document.RepositoryAliases))
	seenAliases := make(map[string]struct{}, len(document.RepositoryAliases))
	for _, alias := range document.RepositoryAliases {
		if !validRegistryName(alias) {
			return MailboxDefinition{}, ErrInvalidConfig
		}
		if _, exists := seenAliases[alias]; exists {
			return MailboxDefinition{}, ErrInvalidConfig
		}
		seenAliases[alias] = struct{}{}
		aliases = append(aliases, alias)
	}

	allowed := make([]string, 0, len(document.AllowedExecution))
	seenContexts := make(map[string]struct{}, len(document.AllowedExecution))
	defaultAllowed := false
	for _, name := range document.AllowedExecution {
		context, exists := contexts[name]
		if !exists || !validRegistryName(name) {
			return MailboxDefinition{}, ErrInvalidConfig
		}
		if _, exists := seenContexts[name]; exists {
			return MailboxDefinition{}, ErrInvalidConfig
		}
		seenContexts[name] = struct{}{}
		if context.Target.Kind() == domain.TargetKindRemote {
			host, exists := hosts[context.Target.Profile()]
			if !exists || host.QueuedBridge == nil {
				return MailboxDefinition{}, ErrInvalidConfig
			}
		}
		if name == document.DefaultExecution {
			defaultAllowed = true
		}
		allowed = append(allowed, name)
	}
	if !defaultAllowed {
		return MailboxDefinition{}, ErrInvalidConfig
	}
	return MailboxDefinition{
		ID: id, Root: document.Root, RepositoryAliases: aliases,
		DefaultExecution: document.DefaultExecution, AllowedExecution: allowed,
		DurableOrphanCleanup: document.DurableOrphanCleanup,
	}, nil
}

func documentAllowsTarget(environment environmentDocument, target targetDocument) bool {
	for _, allowed := range environment.AllowedTargets {
		if allowed == target {
			return true
		}
	}
	return false
}

func validRegistryName(value string) bool { return environmentNamePattern.MatchString(value) }

func validBridgeHost(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 253 || strings.HasPrefix(value, "-") {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z') && !(character >= 'A' && character <= 'Z') &&
			!(character >= '0' && character <= '9') && character != '.' && character != '-' && character != ':' {
			return false
		}
	}
	return true
}

func validPathUnderSecrets(serviceRoot, path string) bool {
	return pathUnder(filepath.Join(serviceRoot, "secrets"), path)
}

func validMailboxRoot(serviceRoot, id, path string) bool {
	expected := filepath.Join(serviceRoot, "mailboxes", id)
	if id == "default" {
		expected = filepath.Join(serviceRoot, "mailbox")
		return path == expected && pathUnder(serviceRoot, path)
	}
	return (path == expected && pathUnder(serviceRoot, path)) || IsExternalMailboxRoot(serviceRoot, path)
}

// IsExternalMailboxRoot reports whether path is a syntactically valid
// non-service-root mailbox location. The filesystem owner, mode, parent, and
// symlink checks happen when runner-local prepares the configured tree.
func IsExternalMailboxRoot(serviceRoot, path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && path != string(filepath.Separator) &&
		!mailboxPathUnder(serviceRoot, path)
}

func sameOrNestedPath(left, right string) bool {
	left = normalizedMailboxPath(left)
	right = normalizedMailboxPath(right)
	if left == right {
		return true
	}
	for _, pair := range [][2]string{{left, right}, {right, left}} {
		relative, err := filepath.Rel(pair[0], pair[1])
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// mailboxPathUnder deliberately uses a conservative case and Unicode-normalized
// comparison. APFS can resolve multiple clean spellings to one directory; it
// is safer to reject a potential service-root alias than to bind two mailbox
// configurations to the same physical tree.
func mailboxPathUnder(root, path string) bool {
	root = normalizedMailboxPath(root)
	path = normalizedMailboxPath(path)
	relative, err := filepath.Rel(root, path)
	return err == nil && (relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))))
}

func normalizedMailboxPath(path string) string {
	return strings.ToLower(norm.NFC.String(path))
}

func validV2HTTPSURL(value string) bool {
	if value == "" || value != strings.TrimSpace(value) {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "https" && parsed.User == nil && parsed.Host != "" &&
		parsed.Hostname() != "" && parsed.Port() == "8443" && parsed.Path == "" && parsed.RawQuery == "" && parsed.Fragment == ""
}

func validV2Bind(value string) bool {
	host, port, err := net.SplitHostPort(value)
	if err != nil || port != "8443" || host == "" {
		return false
	}
	address := net.ParseIP(host)
	return address != nil && !address.IsUnspecified()
}

func synthesizeV1MacRegistries(config *Config) {
	settings, ok := config.MacSettings()
	if !ok {
		return
	}
	localTarget, _ := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
	remoteTarget, _ := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	config.executionContexts["mac-local"] = ExecutionContext{Name: "mac-local", Environment: "mac-dev", Target: localTarget}
	config.executionContexts["ubuntu-current"] = ExecutionContext{Name: "ubuntu-current", Environment: "linux-dev", Target: remoteTarget}

	dispatcherKey, _ := config.SecretReference(SecretDispatcherSSHKey)
	directKey, _ := config.SecretReference(SecretDirectClientTLSKey)
	direct := DirectEndpointProfile{
		Name: settings.RemoteEndpointProfile, TargetProfile: "linux-host", Endpoint: settings.RemoteEndpoint,
		ServerCA: settings.RemoteServerCA, ClientCertificate: settings.DirectClientCertificate, ClientPrivateKeyFile: directKey.File,
	}
	host := RemoteHostProfile{
		Profile: "linux-host", Account: LinuxAccount,
		QueuedBridge: &QueuedBridgeProfile{
			Host: "129.151.232.40", Port: 22, KnownHostsFile: settings.SSHKnownHosts, PrivateKeyFile: dispatcherKey.File,
		},
		DirectEndpoint: &direct,
	}
	config.remoteHosts[host.Profile] = host
	config.directEndpoints[direct.Name] = direct
	config.mailboxes["default"] = MailboxDefinition{
		ID: "default", Root: settings.MailboxRoot, DefaultExecution: "mac-local",
		AllowedExecution: []string{"mac-local", "ubuntu-current"},
	}
}

func cloneRemoteHost(host RemoteHostProfile) RemoteHostProfile {
	if host.QueuedBridge != nil {
		bridge := *host.QueuedBridge
		host.QueuedBridge = &bridge
	}
	if host.DirectEndpoint != nil {
		endpoint := *host.DirectEndpoint
		host.DirectEndpoint = &endpoint
	}
	return host
}

func cloneMailbox(mailbox MailboxDefinition) MailboxDefinition {
	mailbox.RepositoryAliases = append([]string(nil), mailbox.RepositoryAliases...)
	mailbox.AllowedExecution = append([]string(nil), mailbox.AllowedExecution...)
	return mailbox
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
