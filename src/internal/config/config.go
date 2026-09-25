// Package config loads owner-restricted host configuration and its explicit
// environment registry. Secret references are paths; secret contents are
// resolved by later host adapters.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"remote-session-runner/src/internal/domain"
)

const (
	Version                       = 1
	MaxConfigBytes                = 1 << 20
	MacAccount                    = "tomasz.walczuk"
	LinuxAccount                  = "ubuntu"
	MacServiceRoot                = "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner"
	LinuxServiceRoot              = "/home/ubuntu/.local/share/remote-session-runner"
	PublicEndpoint                = "https://129.151.232.40:8443"
	LinuxHTTPSBind                = "10.0.0.200:8443"
	MacEndpointName               = "linux-poc"
	DefaultReconciliationDeadline = 24 * time.Hour
)

const (
	HostKindMac   HostKind = "mac"
	HostKindLinux HostKind = "linux"
)

const (
	SecretDispatcherSSHKey   SecretName = "dispatcher_ssh_key"
	SecretDirectClientTLSKey SecretName = "direct_client_private_key"
	SecretLinuxServerTLSKey  SecretName = "linux_server_private_key"
)

var (
	ErrInvalidConfig       = errors.New("invalid runner configuration")
	ErrConfigFile          = errors.New("configuration file must be an owner-readable regular file")
	ErrConfigOwner         = errors.New("configuration file owner does not match the current user")
	ErrConfigPermissions   = errors.New("configuration file must not be accessible by group or others")
	ErrConfigTooLarge      = errors.New("configuration file exceeds the size limit")
	ErrInvalidSecretRef    = errors.New("invalid secret file reference")
	ErrConfigEnvironment   = errors.New("environment is not configured")
	environmentNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
)

// HostKind identifies the host-specific section present in one config file.
type HostKind string

// SecretName names one of the selected host-side private-key references.
type SecretName string

// SecretReference points to a host-owned secret file. This package never
// opens the referenced file or returns its contents.
type SecretReference struct {
	File string
}

// MacSettings contains the selected Mac service paths and remote client
// endpoint. It is returned by value so callers cannot mutate Config.
type MacSettings struct {
	Account                 string
	ServiceRoot             string
	APISocket               string
	LocalDSocket            string
	Database                string
	MailboxRoot             string
	Workspaces              string
	ScriptTempRoot          string
	Backups                 string
	RemoteEndpointProfile   string
	RemoteEndpoint          string
	SSHHostAlias            string
	SSHKnownHosts           string
	DirectClientCertificate string
	ReconciliationDeadline  time.Duration
}

// LinuxSettings contains the selected Linux service paths and mandatory
// public HTTPS settings. It is returned by value so callers cannot mutate
// Config.
type LinuxSettings struct {
	Account              string
	ServiceRoot          string
	Database             string
	PrivateSocket        string
	Workspaces           string
	ScriptTempRoot       string
	Backups              string
	DirectHTTPSBind      string
	DirectPublicEndpoint string
	ServerCertificate    string
	ClientCA             string
	ClientPrincipalMap   string
	TLSMinVersion        string
	RuntimeAdapter       string
}

// Retention contains metadata, output, and mailbox file-retention defaults.
type Retention struct {
	MetadataAndIdempotency time.Duration
	OutputEvents           time.Duration
	MailboxACKGrace        time.Duration
	MailboxUnacked         time.Duration
}

// RegisteredEnvironment combines the validated domain policy with its
// configured base host system.
type RegisteredEnvironment struct {
	baseSystem string
	policy     domain.Environment
}

// BaseSystem returns the selected underlying host system label.
func (e RegisteredEnvironment) BaseSystem() string { return e.baseSystem }

// Policy returns the immutable validated environment policy.
func (e RegisteredEnvironment) Policy() domain.Environment { return e.policy }

// Config is an immutable, validated host config and environment registry.
type Config struct {
	kind          HostKind
	mac           *MacSettings
	linux         *LinuxSettings
	defaults      domain.ServiceLimits
	retention     Retention
	environments  map[string]RegisteredEnvironment
	secretRefs    map[SecretName]SecretReference
	defaultSource domain.SourceMode
}

// Kind returns which host-specific config section was loaded.
func (c Config) Kind() HostKind { return c.kind }

// MacSettings returns the Mac settings when this is a Mac config.
func (c Config) MacSettings() (MacSettings, bool) {
	if c.mac == nil {
		return MacSettings{}, false
	}
	return *c.mac, true
}

// LinuxSettings returns the Linux settings when this is a Linux config.
func (c Config) LinuxSettings() (LinuxSettings, bool) {
	if c.linux == nil {
		return LinuxSettings{}, false
	}
	return *c.linux, true
}

// Environment returns one configured environment policy.
func (c Config) Environment(name string) (RegisteredEnvironment, bool) {
	environment, ok := c.environments[name]
	return environment, ok
}

// EnvironmentNames returns the registry names in sorted order.
func (c Config) EnvironmentNames() []string {
	names := make([]string, 0, len(c.environments))
	for name := range c.environments {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// DefaultServiceLimits returns the validated host-wide selected defaults.
func (c Config) DefaultServiceLimits() domain.ServiceLimits { return c.defaults }

// Retention returns configured metadata, output, and mailbox retention.
func (c Config) Retention() Retention { return c.retention }

// DefaultSourceMode returns the design-defined default for omitted source.
func (c Config) DefaultSourceMode() domain.SourceMode { return c.defaultSource }

// SecretReference returns a path-only host secret reference by its fixed name.
func (c Config) SecretReference(name SecretName) (SecretReference, bool) {
	reference, ok := c.secretRefs[name]
	return reference, ok
}

// LoadFile opens and validates a configuration file without following
// symlinks, checks that its owner is the current user and its mode excludes
// group/other access, then parses the bounded YAML document.
func LoadFile(path string) (Config, error) {
	if path == "" {
		return Config{}, ErrConfigFile
	}
	linkInfo, err := os.Lstat(path)
	if err != nil || !linkInfo.Mode().IsRegular() {
		return Config{}, ErrConfigFile
	}
	file, err := os.Open(path)
	if err != nil {
		return Config{}, ErrConfigFile
	}
	defer file.Close()

	fileInfo, err := file.Stat()
	if err != nil || !fileInfo.Mode().IsRegular() {
		return Config{}, ErrConfigFile
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || !pathInfo.Mode().IsRegular() || !os.SameFile(fileInfo, pathInfo) {
		return Config{}, ErrConfigFile
	}
	if err := validateConfigFileOwner(fileInfo, uint32(os.Geteuid())); err != nil {
		return Config{}, err
	}
	if fileInfo.Mode().Perm()&0o077 != 0 || fileInfo.Mode().Perm()&0o400 == 0 {
		return Config{}, ErrConfigPermissions
	}
	if fileInfo.Size() < 0 || fileInfo.Size() > MaxConfigBytes {
		return Config{}, ErrConfigTooLarge
	}
	contents, err := io.ReadAll(io.LimitReader(file, MaxConfigBytes+1))
	if err != nil {
		return Config{}, ErrConfigFile
	}
	if len(contents) > MaxConfigBytes {
		return Config{}, ErrConfigTooLarge
	}
	return parse(contents)
}

func validateConfigFileOwner(info os.FileInfo, expectedUID uint32) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint32(stat.Uid) != expectedUID {
		return ErrConfigOwner
	}
	return nil
}

type fileDocument struct {
	Version             int                            `yaml:"version"`
	Mac                 *macDocument                   `yaml:"mac,omitempty"`
	Linux               *linuxDocument                 `yaml:"linux,omitempty"`
	Limits              serviceLimitsDocument          `yaml:"limits,omitempty"`
	Retention           retentionDocument              `yaml:"retention,omitempty"`
	EnvironmentRegistry map[string]environmentDocument `yaml:"environment_registry"`
	SecretReferences    secretReferencesDocument       `yaml:"secret_references,omitempty"`
}

type macDocument struct {
	Account                 string         `yaml:"account"`
	ServiceRoot             string         `yaml:"service_root"`
	APISocket               string         `yaml:"api_socket"`
	LocalDSocket            string         `yaml:"locald_socket"`
	Database                string         `yaml:"sqlite"`
	MailboxRoot             string         `yaml:"mailbox_root"`
	Workspaces              string         `yaml:"workspaces"`
	ScriptTempRoot          string         `yaml:"script_temp_root"`
	Backups                 string         `yaml:"backups"`
	RemoteEndpointProfile   string         `yaml:"remote_endpoint_profile"`
	RemoteEndpoint          string         `yaml:"remote_endpoint"`
	SSHHostAlias            string         `yaml:"ssh_host_alias"`
	SSHKnownHosts           string         `yaml:"ssh_known_hosts"`
	DirectClientCertificate string         `yaml:"direct_client_certificate"`
	ReconciliationDeadline  *durationValue `yaml:"reconciliation_deadline,omitempty"`
}

type linuxDocument struct {
	Account              string `yaml:"account"`
	ServiceRoot          string `yaml:"service_root"`
	Database             string `yaml:"sqlite"`
	PrivateSocket        string `yaml:"private_socket"`
	Workspaces           string `yaml:"workspaces"`
	ScriptTempRoot       string `yaml:"script_temp_root"`
	Backups              string `yaml:"backups"`
	DirectHTTPSBind      string `yaml:"direct_https_bind"`
	DirectPublicEndpoint string `yaml:"direct_public_endpoint"`
	ServerCertificate    string `yaml:"server_cert"`
	ClientCA             string `yaml:"client_ca"`
	ClientPrincipalMap   string `yaml:"client_principal_map"`
	TLSMinVersion        string `yaml:"tls_min_version"`
	RuntimeAdapter       string `yaml:"runtime_adapter"`
}

type secretReferencesDocument struct {
	DispatcherSSHKey *secretReferenceDocument `yaml:"dispatcher_ssh_key,omitempty"`
	DirectClientKey  *secretReferenceDocument `yaml:"direct_client_private_key,omitempty"`
	LinuxServerKey   *secretReferenceDocument `yaml:"linux_server_private_key,omitempty"`
}

type secretReferenceDocument struct {
	File string `yaml:"file"`
}

type targetDocument struct {
	Kind    domain.TargetKind `yaml:"kind"`
	Profile string            `yaml:"profile"`
}

type controllerDocument struct {
	Type domain.ControllerType `yaml:"type"`
	ID   string                `yaml:"id"`
}

type environmentDocument struct {
	BaseSystem               string                `yaml:"base_system"`
	HostClass                string                `yaml:"host_class"`
	EffectiveAccount         string                `yaml:"effective_account"`
	AllowedTargets           []targetDocument      `yaml:"allowed_targets"`
	AllowedSourceModes       []domain.SourceMode   `yaml:"allowed_source_modes"`
	AllowedRepositoryAliases []string              `yaml:"allowed_repository_aliases"`
	AllowedControllers       []controllerDocument  `yaml:"allowed_controllers"`
	ServiceLimits            serviceLimitsDocument `yaml:"service_limits,omitempty"`
}

type serviceLimitsDocument struct {
	ActiveSessionsPerHost  *int           `yaml:"active_sessions_per_host,omitempty"`
	RunningCommandsPerHost *int           `yaml:"running_commands_per_host,omitempty"`
	SerializedRequestBytes *int64         `yaml:"serialized_request_bytes,omitempty"`
	ScriptBytesPerRequest  *int64         `yaml:"script_bytes_per_request,omitempty"`
	CommandTimeout         *durationValue `yaml:"command_timeout,omitempty"`
	IdleTimeout            *durationValue `yaml:"idle_timeout,omitempty"`
	SessionMaxLifetime     *durationValue `yaml:"session_max_lifetime,omitempty"`
	OutputBytesPerCommand  *int64         `yaml:"output_bytes_per_command,omitempty"`
	SubscriberBufferBytes  *int64         `yaml:"subscriber_buffer_bytes,omitempty"`
	PersistenceQueueBytes  *int64         `yaml:"persistence_queue_bytes,omitempty"`
}

type retentionDocument struct {
	MetadataAndIdempotency *durationValue `yaml:"metadata_and_idempotency,omitempty"`
	OutputEvents           *durationValue `yaml:"output_events,omitempty"`
	MailboxACKGrace        *durationValue `yaml:"mailbox_ack_grace,omitempty"`
	MailboxUnacked         *durationValue `yaml:"mailbox_unacked,omitempty"`
}

type durationValue time.Duration

func (d *durationValue) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return errors.New("duration must be a string")
	}
	parsed, err := parseDuration(node.Value)
	if err != nil {
		return errors.New("invalid duration")
	}
	*d = durationValue(parsed)
	return nil
}

func parseDuration(value string) (time.Duration, error) {
	if strings.HasSuffix(value, "d") {
		daysText := strings.TrimSuffix(value, "d")
		days, err := strconv.ParseInt(daysText, 10, 64)
		if err != nil || days <= 0 || days > int64((1<<63-1)/int64(24*time.Hour)) {
			return 0, errors.New("invalid day duration")
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	return time.ParseDuration(value)
}

func parse(contents []byte) (Config, error) {
	if len(contents) == 0 {
		return Config{}, ErrInvalidConfig
	}
	if len(contents) > MaxConfigBytes {
		return Config{}, ErrConfigTooLarge
	}
	if err := validateYAMLDocument(contents); err != nil {
		return Config{}, ErrInvalidConfig
	}
	var document fileDocument
	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	decoder.KnownFields(true)
	if err := decoder.Decode(&document); err != nil {
		// Parser errors may include scalar values. Keep them out of returned
		// errors so an accidentally inlined secret cannot be copied to logs.
		return Config{}, ErrInvalidConfig
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Config{}, ErrInvalidConfig
	}
	return validateDocument(document)
}

func validateYAMLDocument(contents []byte) error {
	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return err
	}
	if err := decoder.Decode(&yaml.Node{}); err != io.EOF {
		return errors.New("multiple YAML documents")
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return errors.New("configuration root must be a mapping")
	}
	var walk func(*yaml.Node) error
	walk = func(node *yaml.Node) error {
		if node.Anchor != "" || node.Kind == yaml.AliasNode {
			return errors.New("YAML aliases are not allowed")
		}
		switch node.Tag {
		case "!!str", "!!int", "!!bool", "!!float", "!!map", "!!seq":
		default:
			return errors.New("unsupported YAML tag")
		}
		for _, child := range node.Content {
			if err := walk(child); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(document.Content[0])
}

func validateDocument(document fileDocument) (Config, error) {
	if document.Version != Version || (document.Mac == nil) == (document.Linux == nil) || len(document.EnvironmentRegistry) != 2 {
		return Config{}, ErrInvalidConfig
	}
	if err := validateSelectedRegistry(document.EnvironmentRegistry); err != nil {
		return Config{}, err
	}
	defaults := domain.DefaultServiceLimits()
	if err := applyServiceLimits(&defaults, document.Limits); err != nil {
		return Config{}, err
	}
	retention := Retention{
		MetadataAndIdempotency: defaults.MetadataRetention,
		OutputEvents:           defaults.OutputRetention,
		MailboxACKGrace:        24 * time.Hour,
		MailboxUnacked:         7 * 24 * time.Hour,
	}
	if document.Retention.MetadataAndIdempotency != nil {
		retention.MetadataAndIdempotency = time.Duration(*document.Retention.MetadataAndIdempotency)
	}
	if document.Retention.OutputEvents != nil {
		retention.OutputEvents = time.Duration(*document.Retention.OutputEvents)
	}
	if document.Retention.MailboxACKGrace != nil {
		retention.MailboxACKGrace = time.Duration(*document.Retention.MailboxACKGrace)
	}
	if document.Retention.MailboxUnacked != nil {
		retention.MailboxUnacked = time.Duration(*document.Retention.MailboxUnacked)
	}
	if retention.MetadataAndIdempotency < 90*24*time.Hour || retention.OutputEvents <= 0 || retention.MailboxACKGrace <= 0 || retention.MailboxUnacked <= 0 {
		return Config{}, ErrInvalidConfig
	}
	defaults.MetadataRetention = retention.MetadataAndIdempotency
	defaults.OutputRetention = retention.OutputEvents
	if document.Mac != nil {
		if err := validateMacDocument(document.Mac, document.SecretReferences); err != nil {
			return Config{}, err
		}
	} else if err := validateLinuxDocument(document.Linux, document.SecretReferences); err != nil {
		return Config{}, err
	}

	config := Config{
		defaults:      defaults,
		retention:     retention,
		environments:  make(map[string]RegisteredEnvironment, len(document.EnvironmentRegistry)),
		secretRefs:    make(map[SecretName]SecretReference),
		defaultSource: domain.SourceModeEmpty,
	}
	if document.Mac != nil {
		config.kind = HostKindMac
		settings := macSettings(document.Mac)
		config.mac = &settings
		config.secretRefs[SecretDispatcherSSHKey] = SecretReference{File: document.SecretReferences.DispatcherSSHKey.File}
		config.secretRefs[SecretDirectClientTLSKey] = SecretReference{File: document.SecretReferences.DirectClientKey.File}
	} else {
		config.kind = HostKindLinux
		settings := linuxSettings(document.Linux)
		config.linux = &settings
		config.secretRefs[SecretLinuxServerTLSKey] = SecretReference{File: document.SecretReferences.LinuxServerKey.File}
	}

	for name, spec := range document.EnvironmentRegistry {
		if !environmentNamePattern.MatchString(name) || strings.TrimSpace(spec.BaseSystem) == "" || strings.TrimSpace(spec.HostClass) == "" || strings.TrimSpace(spec.EffectiveAccount) == "" {
			return Config{}, ErrInvalidConfig
		}
		targets := make([]domain.ExecutionTarget, 0, len(spec.AllowedTargets))
		for _, target := range spec.AllowedTargets {
			validated, err := domain.NewExecutionTarget(target.Kind, target.Profile)
			if err != nil {
				return Config{}, ErrInvalidConfig
			}
			targets = append(targets, validated)
		}
		controllers := make([]domain.ControllerIdentity, 0, len(spec.AllowedControllers))
		for _, controller := range spec.AllowedControllers {
			id, err := domain.NewControllerID(controller.ID)
			if err != nil {
				return Config{}, ErrInvalidConfig
			}
			validated, err := domain.NewControllerIdentity(controller.Type, id)
			if err != nil {
				return Config{}, ErrInvalidConfig
			}
			controllers = append(controllers, validated)
		}
		limits := defaults
		if err := applyServiceLimits(&limits, spec.ServiceLimits); err != nil {
			return Config{}, err
		}
		environmentPolicy, err := domain.NewEnvironment(domain.EnvironmentSpec{
			Name:                     name,
			HostClass:                spec.HostClass,
			EffectiveAccount:         spec.EffectiveAccount,
			AllowedTargets:           targets,
			AllowedSourceModes:       append([]domain.SourceMode(nil), spec.AllowedSourceModes...),
			AllowedRepositoryAliases: append([]string(nil), spec.AllowedRepositoryAliases...),
			AllowedControllers:       controllers,
			ServiceLimits:            limits,
		})
		if err != nil {
			return Config{}, ErrInvalidConfig
		}
		config.environments[name] = RegisteredEnvironment{baseSystem: spec.BaseSystem, policy: environmentPolicy}
	}
	return config, nil
}

func validateSelectedRegistry(environments map[string]environmentDocument) error {
	mac, macOK := environments["mac-dev"]
	linux, linuxOK := environments["linux-dev"]
	if !macOK || !linuxOK {
		return ErrInvalidConfig
	}
	if mac.BaseSystem != "macOS" || mac.HostClass != "macOS workstation" || mac.EffectiveAccount != MacAccount ||
		!sameTargets(mac.AllowedTargets, targetDocument{Kind: domain.TargetKindLocal, Profile: "mac-workstation"}) ||
		!sameStrings(sourceModeStrings(mac.AllowedSourceModes), []string{"empty", "local_worktree"}) ||
		len(mac.AllowedRepositoryAliases) != 0 ||
		!sameControllers(mac.AllowedControllers, controllerDocument{Type: domain.ControllerTypeLocalUser, ID: MacAccount}) {
		return ErrInvalidConfig
	}
	if linux.BaseSystem != "Ubuntu 20.04.6 LTS" || linux.HostClass != "Ubuntu Linux host" || linux.EffectiveAccount != LinuxAccount ||
		!sameTargets(linux.AllowedTargets, targetDocument{Kind: domain.TargetKindRemote, Profile: "linux-host"}) ||
		!sameStrings(sourceModeStrings(linux.AllowedSourceModes), []string{"empty"}) || len(linux.AllowedRepositoryAliases) != 0 ||
		!sameControllers(linux.AllowedControllers,
			controllerDocument{Type: domain.ControllerTypeQueuedMac, ID: MacAccount},
			controllerDocument{Type: domain.ControllerTypeDirectMTLS, ID: MacAccount}) {
		return ErrInvalidConfig
	}
	return nil
}

func sameTargets(got []targetDocument, want ...targetDocument) bool {
	if len(got) != len(want) {
		return false
	}
	for _, expected := range want {
		found := false
		for _, target := range got {
			if target == expected {
				if found {
					return false
				}
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func sameControllers(got []controllerDocument, want ...controllerDocument) bool {
	if len(got) != len(want) {
		return false
	}
	for _, expected := range want {
		found := false
		for _, controller := range got {
			if controller == expected {
				if found {
					return false
				}
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func sourceModeStrings(modes []domain.SourceMode) []string {
	result := make([]string, len(modes))
	for i, mode := range modes {
		result[i] = string(mode)
	}
	return result
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	counts := make(map[string]int, len(left))
	for _, value := range left {
		counts[value]++
	}
	for _, value := range right {
		counts[value]--
	}
	for _, count := range counts {
		if count != 0 {
			return false
		}
	}
	return true
}

func validateMacDocument(mac *macDocument, refs secretReferencesDocument) error {
	if mac.Account != MacAccount || mac.ServiceRoot != MacServiceRoot || !validServiceRoot(mac.ServiceRoot) || mac.RemoteEndpointProfile != MacEndpointName || !validateEndpoint(mac.RemoteEndpoint) ||
		mac.SSHHostAlias != "remote-session-runner" || (mac.ReconciliationDeadline != nil && mac.ReconciliationDeadline.Duration() <= 0) {
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
		servicePath{mac.SSHKnownHosts, "secrets/ssh_known_hosts"},
		servicePath{mac.DirectClientCertificate, "secrets/direct-client.pem"},
	) {
		return ErrInvalidConfig
	}
	if refs.DispatcherSSHKey == nil || refs.DirectClientKey == nil || refs.LinuxServerKey != nil ||
		!validSecretRef(mac.ServiceRoot, refs.DispatcherSSHKey) || !validSecretRef(mac.ServiceRoot, refs.DirectClientKey) {
		return ErrInvalidSecretRef
	}
	return nil
}

func validateLinuxDocument(linux *linuxDocument, refs secretReferencesDocument) error {
	if linux.Account != LinuxAccount || linux.ServiceRoot != LinuxServiceRoot || !validServiceRoot(linux.ServiceRoot) || linux.DirectHTTPSBind != LinuxHTTPSBind ||
		!validateEndpoint(linux.DirectPublicEndpoint) || linux.TLSMinVersion != "1.3" || linux.RuntimeAdapter != "linux-host-process" {
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
	return nil
}

type servicePath struct {
	configured string
	relative   string
}

func selectedPaths(root string, paths ...servicePath) bool {
	for _, path := range paths {
		if path.configured != filepath.Join(root, filepath.FromSlash(path.relative)) || !pathUnder(root, path.configured) {
			return false
		}
	}
	return true
}

func validServiceRoot(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && path != string(filepath.Separator)
}

func pathUnder(root, path string) bool {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func validSecretRef(serviceRoot string, reference *secretReferenceDocument) bool {
	return reference != nil && pathUnder(filepath.Join(serviceRoot, "secrets"), reference.File)
}

func applyServiceLimits(limits *domain.ServiceLimits, document serviceLimitsDocument) error {
	if document.ActiveSessionsPerHost != nil {
		limits.ActiveSessionsPerHost = *document.ActiveSessionsPerHost
	}
	if document.RunningCommandsPerHost != nil {
		limits.RunningCommandsPerHost = *document.RunningCommandsPerHost
	}
	if document.SerializedRequestBytes != nil {
		if *document.SerializedRequestBytes != domain.MaxSerializedRequestBytes {
			return ErrInvalidConfig
		}
		limits.SerializedRequestBytes = *document.SerializedRequestBytes
	}
	if document.ScriptBytesPerRequest != nil {
		if *document.ScriptBytesPerRequest != domain.MaxScriptUTF8Bytes {
			return ErrInvalidConfig
		}
		limits.ScriptBytesPerRequest = *document.ScriptBytesPerRequest
	}
	if document.CommandTimeout != nil {
		limits.CommandTimeout = time.Duration(*document.CommandTimeout)
	}
	if document.IdleTimeout != nil {
		limits.IdleTimeout = time.Duration(*document.IdleTimeout)
	}
	if document.SessionMaxLifetime != nil {
		limits.SessionMaxLifetime = time.Duration(*document.SessionMaxLifetime)
	}
	if document.OutputBytesPerCommand != nil {
		limits.OutputBytesPerCommand = *document.OutputBytesPerCommand
	}
	if document.SubscriberBufferBytes != nil {
		limits.SubscriberBufferBytes = *document.SubscriberBufferBytes
	}
	if document.PersistenceQueueBytes != nil {
		limits.PersistenceQueueBytes = *document.PersistenceQueueBytes
	}
	return nil
}

func macSettings(document *macDocument) MacSettings {
	reconciliationDeadline := DefaultReconciliationDeadline
	if document.ReconciliationDeadline != nil {
		reconciliationDeadline = time.Duration(*document.ReconciliationDeadline)
	}
	return MacSettings{
		Account:                 document.Account,
		ServiceRoot:             document.ServiceRoot,
		APISocket:               document.APISocket,
		LocalDSocket:            document.LocalDSocket,
		Database:                document.Database,
		MailboxRoot:             document.MailboxRoot,
		Workspaces:              document.Workspaces,
		ScriptTempRoot:          document.ScriptTempRoot,
		Backups:                 document.Backups,
		RemoteEndpointProfile:   document.RemoteEndpointProfile,
		RemoteEndpoint:          document.RemoteEndpoint,
		SSHHostAlias:            document.SSHHostAlias,
		SSHKnownHosts:           document.SSHKnownHosts,
		DirectClientCertificate: document.DirectClientCertificate,
		ReconciliationDeadline:  reconciliationDeadline,
	}
}

func linuxSettings(document *linuxDocument) LinuxSettings {
	return LinuxSettings{
		Account:              document.Account,
		ServiceRoot:          document.ServiceRoot,
		Database:             document.Database,
		PrivateSocket:        document.PrivateSocket,
		Workspaces:           document.Workspaces,
		ScriptTempRoot:       document.ScriptTempRoot,
		Backups:              document.Backups,
		DirectHTTPSBind:      document.DirectHTTPSBind,
		DirectPublicEndpoint: document.DirectPublicEndpoint,
		ServerCertificate:    document.ServerCertificate,
		ClientCA:             document.ClientCA,
		ClientPrincipalMap:   document.ClientPrincipalMap,
		TLSMinVersion:        document.TLSMinVersion,
		RuntimeAdapter:       document.RuntimeAdapter,
	}
}

func validateEndpoint(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && value == PublicEndpoint && parsed.Scheme == "https" && parsed.User == nil && parsed.Host == "129.151.232.40:8443" && parsed.Path == "" && parsed.RawQuery == "" && parsed.Fragment == ""
}

func (d durationValue) Duration() time.Duration { return time.Duration(d) }

func (c Config) String() string {
	// Do not include configured paths or secret references in diagnostics.
	return fmt.Sprintf("runner config for %s (%d environments)", c.kind, len(c.environments))
}
