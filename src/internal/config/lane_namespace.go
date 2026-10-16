package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"remote-session-runner/src/internal/domain"
)

// ReleaseLane is the immutable deployment identity added by V3. It is
// intentionally distinct from V2 execution environment names such as
// "linux-dev".
type ReleaseLane string

const (
	ReleaseLaneDev ReleaseLane = "dev"
	ReleaseLaneQAS ReleaseLane = "qas"
	ReleaseLanePRD ReleaseLane = "prd"
)

var ErrInvalidLaneIdentity = errors.New("invalid V3 lane identity or namespace")

// LaneNamespace contains the fixed paths and service/network names derived
// from a V3 release lane. Empty platform-specific fields mean that the
// resource does not exist for that host kind; in particular, PRD continues to
// use the existing host-global Linux SSH listener and cgroup scope.
type LaneNamespace struct {
	Lane          ReleaseLane
	Host          HostKind
	RemoteProfile string

	Account         string
	ServiceRoot     string
	ConfigFile      string
	Database        string
	DatabaseWAL     string
	DatabaseSHM     string
	APISocket       string
	LocalDSocket    string
	PrivateSocket   string
	MailboxRoot     string
	Workspaces      string
	ScriptTempRoot  string
	Backups         string
	Logs            string
	Binaries        string
	Staging         string
	LifecycleLock   string
	RegistryRoot    string
	RunnerBinary    string
	LocalBinary     string
	LocalDBinary    string
	QueuedSSHBinary string
	SessionAgent    string
	LogCapture      string

	LocalAgentLabel  string
	LocalDAgentLabel string
	LocalAgentPlist  string
	LocalDAgentPlist string

	RunnerUnit         string
	RunnerEntrypoint   string
	JournalIdentifier  string
	QueuedSSHJournal   string
	RunnerSlice        string
	QueuedSSHUnit      string
	SSHAuthorityPath   string
	QueuePort          int
	DirectPort         int
	DirectEndpointName string
	DirectEndpointURL  string
	DirectHTTPSBind    string
}

// DeriveLaneNamespace returns the only namespace accepted for the selected
// host, release lane, and (for Linux) remote profile. It performs no filesystem
// access and accepts no caller-controlled path, label, unit, or port.
func DeriveLaneNamespace(host HostKind, lane ReleaseLane, remoteProfile string) (LaneNamespace, error) {
	if !validReleaseLane(lane) {
		return LaneNamespace{}, ErrInvalidLaneIdentity
	}
	suffix := ""
	switch lane {
	case ReleaseLaneDev:
		suffix = "-dev"
	case ReleaseLaneQAS:
		suffix = "-qas"
	}

	switch host {
	case HostKindMac:
		if remoteProfile != "" {
			return LaneNamespace{}, ErrInvalidLaneIdentity
		}
		root := filepath.Join("/Users", MacAccount, "Library", "Application Support", "RemoteSessionRunner"+suffix)
		launchAgents := filepath.Join("/Users", MacAccount, "Library", "LaunchAgents")
		registryRoot := filepath.Join("/Users", MacAccount, "Library", "Application Support", "RemoteSessionRunner-lane-registry")
		localLabel := "com.remote-session-runner.local" + suffix
		localdLabel := "com.remote-session-runner.locald" + suffix
		database := filepath.Join(root, "state", "local.db")
		return LaneNamespace{
			Lane: lane, Host: host, Account: MacAccount,
			ServiceRoot: root, ConfigFile: filepath.Join(root, "config", "mac.yaml"),
			Database: database, DatabaseWAL: database + "-wal", DatabaseSHM: database + "-shm",
			APISocket:       filepath.Join(root, "run", "local-api.sock"),
			LocalDSocket:    filepath.Join(root, "run", "locald.sock"),
			MailboxRoot:     filepath.Join(root, "mailbox"),
			Workspaces:      filepath.Join(root, "workspaces"),
			ScriptTempRoot:  filepath.Join(root, "tmp", "scripts"),
			Backups:         filepath.Join(root, "backups"),
			Logs:            filepath.Join(root, "logs"),
			Binaries:        filepath.Join(root, "bin"),
			Staging:         filepath.Join(root, "staging"),
			LifecycleLock:   filepath.Join(root, "run", "runner-locald-lifecycle.lock"),
			RegistryRoot:    registryRoot,
			RunnerBinary:    filepath.Join(root, "bin", "runner"),
			LocalBinary:     filepath.Join(root, "bin", "runner-local"),
			LocalDBinary:    filepath.Join(root, "bin", "runner-locald"),
			LogCapture:      filepath.Join(root, "bin", "runner-log-capture"),
			LocalAgentLabel: localLabel, LocalDAgentLabel: localdLabel,
			LocalAgentPlist:  filepath.Join(launchAgents, localLabel+".plist"),
			LocalDAgentPlist: filepath.Join(launchAgents, localdLabel+".plist"),
		}, nil
	case HostKindLinux:
		profile, ok := laneRemoteProfiles[remoteProfile]
		if !ok {
			return LaneNamespace{}, ErrInvalidLaneIdentity
		}
		root := filepath.Join("/home", LinuxAccount, ".local", "share", "remote-session-runner"+suffix)
		binaryRoot := filepath.Join(root, "bin")
		queuePort, directPort := 22, 8443
		queueUnit, slice, sshAuthority := "", "", filepath.Join("/home", LinuxAccount, ".ssh", "authorized_keys")
		queueJournal := "sshd"
		if lane != ReleaseLanePRD {
			queuePort = 2224
			if lane == ReleaseLaneQAS {
				queuePort = 2225
			}
			directPort = 8444
			if lane == ReleaseLaneQAS {
				directPort = 8445
			}
			queueUnit = "runner-sshd" + suffix + ".service"
			queueJournal = "runner-sshd" + suffix
			slice = "remote-session-runner" + suffix + ".slice"
			sshAuthority = filepath.Join("/etc", "remote-session-runner", "ssh", string(lane))
		}
		endpointName := profile.endpointPrefix + "-poc"
		if suffix != "" {
			endpointName += suffix
		}
		endpoint := "https://" + profile.publicIP + ":" + strconv.Itoa(directPort)
		bind := profile.privateIP + ":" + strconv.Itoa(directPort)
		database := filepath.Join(root, "state", "remote.db")
		runnerUnit := "runnerd" + suffix + ".service"
		return LaneNamespace{
			Lane: lane, Host: host, RemoteProfile: remoteProfile, Account: LinuxAccount,
			ServiceRoot: root, ConfigFile: filepath.Join(root, "config", "linux.yaml"),
			Database: database, DatabaseWAL: database + "-wal", DatabaseSHM: database + "-shm",
			PrivateSocket:   filepath.Join(root, "run", "runnerd.sock"),
			Workspaces:      filepath.Join(root, "workspaces"),
			ScriptTempRoot:  filepath.Join(root, "tmp", "scripts"),
			Backups:         filepath.Join(root, "backups"),
			Logs:            filepath.Join(root, "logs"),
			Binaries:        binaryRoot,
			Staging:         filepath.Join(root, "staging"),
			LifecycleLock:   filepath.Join(root, "run", "runnerd-lifecycle.lock"),
			RunnerBinary:    filepath.Join(binaryRoot, "runnerd"),
			QueuedSSHBinary: filepath.Join(binaryRoot, "runner-ssh-bridge"),
			SessionAgent:    filepath.Join(binaryRoot, "runner-session-agent"),
			LogCapture:      filepath.Join(binaryRoot, "runner-log-capture"),
			RunnerUnit:      runnerUnit, RunnerEntrypoint: filepath.Join(binaryRoot, "runnerd-entrypoint.sh"),
			JournalIdentifier: strings.TrimSuffix(runnerUnit, ".service"),
			QueuedSSHJournal:  queueJournal,
			RunnerSlice:       slice, QueuedSSHUnit: queueUnit,
			SSHAuthorityPath: sshAuthority, QueuePort: queuePort, DirectPort: directPort,
			DirectEndpointName: endpointName, DirectEndpointURL: endpoint, DirectHTTPSBind: bind,
		}, nil
	default:
		return LaneNamespace{}, ErrInvalidLaneIdentity
	}
}

type laneRemoteProfile struct {
	endpointPrefix string
	publicIP       string
	privateIP      string
}

var laneRemoteProfiles = map[string]laneRemoteProfile{
	"linux-host":      {endpointPrefix: "linux", publicIP: "129.151.232.40", privateIP: "10.0.0.200"},
	"sandbox-host":    {endpointPrefix: "sandbox", publicIP: "132.226.205.205", privateIP: "10.0.0.14"},
	"factory-sandbox": {endpointPrefix: "factory-sandbox", publicIP: "145.241.255.66", privateIP: "10.0.0.30"},
}

func validReleaseLane(lane ReleaseLane) bool {
	return lane == ReleaseLaneDev || lane == ReleaseLaneQAS || lane == ReleaseLanePRD
}

// ValidateV3Target enforces the first-rollout target boundary before profile
// resolution. Dev and QAS may execute submitted scripts only on an accepted
// remote profile. PRD retains its existing Mac-local target behavior.
func ValidateV3Target(lane ReleaseLane, kind domain.TargetKind, profile string) error {
	if !validReleaseLane(lane) {
		return ErrInvalidLaneIdentity
	}
	switch kind {
	case domain.TargetKindLocal:
		if lane != ReleaseLanePRD || profile != "mac-workstation" {
			return ErrInvalidLaneIdentity
		}
	case domain.TargetKindRemote:
		if _, ok := laneRemoteProfiles[profile]; !ok {
			return ErrInvalidLaneIdentity
		}
	default:
		return ErrInvalidLaneIdentity
	}
	return nil
}

type v3NamespaceDocument struct {
	Version     int              `yaml:"version"`
	ReleaseLane ReleaseLane      `yaml:"release_lane"`
	Mac         *v3MacDocument   `yaml:"mac,omitempty"`
	Linux       *v3LinuxDocument `yaml:"linux,omitempty"`
}

type v3MacDocument struct {
	Account   string            `yaml:"account"`
	Namespace v3NamespaceFields `yaml:"namespace"`
}

type v3LinuxDocument struct {
	Account       string            `yaml:"account"`
	RemoteProfile string            `yaml:"remote_profile"`
	Namespace     v3NamespaceFields `yaml:"namespace"`
}

// v3NamespaceFields is deliberately a closed configuration shape. Paths,
// service labels, and ports are assertions checked against the derived fixed
// namespace; they are never instructions to create or redirect a resource.
type v3NamespaceFields struct {
	ServiceRoot        string `yaml:"service_root"`
	ConfigFile         string `yaml:"config_file"`
	Database           string `yaml:"database"`
	DatabaseWAL        string `yaml:"database_wal"`
	DatabaseSHM        string `yaml:"database_shm"`
	APISocket          string `yaml:"api_socket,omitempty"`
	LocalDSocket       string `yaml:"locald_socket,omitempty"`
	PrivateSocket      string `yaml:"private_socket,omitempty"`
	MailboxRoot        string `yaml:"mailbox_root,omitempty"`
	Workspaces         string `yaml:"workspaces"`
	ScriptTempRoot     string `yaml:"script_temp_root"`
	Backups            string `yaml:"backups"`
	Logs               string `yaml:"logs"`
	Binaries           string `yaml:"binaries"`
	Staging            string `yaml:"staging"`
	LifecycleLock      string `yaml:"lifecycle_lock"`
	RegistryRoot       string `yaml:"registry_root,omitempty"`
	RunnerBinary       string `yaml:"runner_binary"`
	LocalBinary        string `yaml:"local_binary,omitempty"`
	LocalDBinary       string `yaml:"locald_binary,omitempty"`
	QueuedSSHBinary    string `yaml:"queued_ssh_binary,omitempty"`
	SessionAgent       string `yaml:"session_agent_binary,omitempty"`
	LogCapture         string `yaml:"log_capture_binary"`
	LocalAgentLabel    string `yaml:"local_agent_label,omitempty"`
	LocalDAgentLabel   string `yaml:"locald_agent_label,omitempty"`
	LocalAgentPlist    string `yaml:"local_agent_plist,omitempty"`
	LocalDAgentPlist   string `yaml:"locald_agent_plist,omitempty"`
	RunnerUnit         string `yaml:"runner_unit,omitempty"`
	RunnerEntrypoint   string `yaml:"runner_entrypoint,omitempty"`
	JournalIdentifier  string `yaml:"journal_identifier,omitempty"`
	QueuedSSHJournal   string `yaml:"queued_ssh_journal_identifier,omitempty"`
	RunnerSlice        string `yaml:"runner_slice,omitempty"`
	QueuedSSHUnit      string `yaml:"queued_ssh_unit,omitempty"`
	SSHAuthorityPath   string `yaml:"ssh_authority_path,omitempty"`
	QueuePort          int    `yaml:"queue_port,omitempty"`
	DirectPort         int    `yaml:"direct_port,omitempty"`
	DirectEndpointName string `yaml:"direct_endpoint_name,omitempty"`
	DirectEndpointURL  string `yaml:"direct_endpoint_url,omitempty"`
	DirectHTTPSBind    string `yaml:"direct_https_bind,omitempty"`
}

// ParseV3Namespace validates the version/lane/host identity and verifies every
// declared namespace value against DeriveLaneNamespace. This is the closed
// namespace portion of V3; execution contexts, mailbox definitions, and
// credentials are added by later L1 units. The general V1/V2 loader remains
// unchanged until that complete V3 document is assembled.
func ParseV3Namespace(contents []byte) (LaneNamespace, error) {
	if len(contents) == 0 || len(contents) > MaxConfigBytes {
		return LaneNamespace{}, ErrInvalidLaneIdentity
	}
	if err := validateYAMLDocument(contents); err != nil {
		return LaneNamespace{}, ErrInvalidLaneIdentity
	}
	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	decoder.KnownFields(true)
	var document v3NamespaceDocument
	if err := decoder.Decode(&document); err != nil {
		return LaneNamespace{}, ErrInvalidLaneIdentity
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return LaneNamespace{}, ErrInvalidLaneIdentity
	}
	if document.Version != VersionV3 || !validReleaseLane(document.ReleaseLane) || (document.Mac == nil) == (document.Linux == nil) {
		return LaneNamespace{}, ErrInvalidLaneIdentity
	}

	var expected LaneNamespace
	var declared v3NamespaceFields
	if document.Mac != nil {
		if document.Mac.Account != MacAccount {
			return LaneNamespace{}, ErrInvalidLaneIdentity
		}
		expected, _ = DeriveLaneNamespace(HostKindMac, document.ReleaseLane, "")
		declared = document.Mac.Namespace
	} else {
		if document.Linux.Account != LinuxAccount {
			return LaneNamespace{}, ErrInvalidLaneIdentity
		}
		expected, _ = DeriveLaneNamespace(HostKindLinux, document.ReleaseLane, document.Linux.RemoteProfile)
		if expected.Host == "" {
			return LaneNamespace{}, ErrInvalidLaneIdentity
		}
		declared = document.Linux.Namespace
	}
	if !namespaceFieldsMatch(declared, expected) {
		return LaneNamespace{}, ErrInvalidLaneIdentity
	}
	return expected, nil
}

func namespaceFieldsMatch(got v3NamespaceFields, want LaneNamespace) bool {
	return got.ServiceRoot == want.ServiceRoot && got.ConfigFile == want.ConfigFile && got.Database == want.Database &&
		got.DatabaseWAL == want.DatabaseWAL && got.DatabaseSHM == want.DatabaseSHM &&
		got.APISocket == want.APISocket && got.LocalDSocket == want.LocalDSocket && got.PrivateSocket == want.PrivateSocket &&
		got.MailboxRoot == want.MailboxRoot && got.Workspaces == want.Workspaces && got.ScriptTempRoot == want.ScriptTempRoot &&
		got.Backups == want.Backups && got.Logs == want.Logs && got.Binaries == want.Binaries && got.Staging == want.Staging &&
		got.LifecycleLock == want.LifecycleLock && got.RegistryRoot == want.RegistryRoot && got.RunnerBinary == want.RunnerBinary &&
		got.LocalBinary == want.LocalBinary && got.LocalDBinary == want.LocalDBinary && got.QueuedSSHBinary == want.QueuedSSHBinary &&
		got.SessionAgent == want.SessionAgent && got.LogCapture == want.LogCapture &&
		got.LocalAgentLabel == want.LocalAgentLabel && got.LocalDAgentLabel == want.LocalDAgentLabel &&
		got.LocalAgentPlist == want.LocalAgentPlist && got.LocalDAgentPlist == want.LocalDAgentPlist &&
		got.RunnerUnit == want.RunnerUnit && got.RunnerEntrypoint == want.RunnerEntrypoint &&
		got.JournalIdentifier == want.JournalIdentifier && got.QueuedSSHJournal == want.QueuedSSHJournal &&
		got.RunnerSlice == want.RunnerSlice && got.QueuedSSHUnit == want.QueuedSSHUnit &&
		got.SSHAuthorityPath == want.SSHAuthorityPath && got.QueuePort == want.QueuePort && got.DirectPort == want.DirectPort &&
		got.DirectEndpointName == want.DirectEndpointName && got.DirectEndpointURL == want.DirectEndpointURL &&
		got.DirectHTTPSBind == want.DirectHTTPSBind
}

// NamespaceDocumentYAML produces a stable, reviewable V3 namespace document.
// It is intended for source fixtures and future config generation only; it
// does not write or install any file.
func NamespaceDocumentYAML(namespace LaneNamespace) ([]byte, error) {
	expected, err := DeriveLaneNamespace(namespace.Host, namespace.Lane, namespace.RemoteProfile)
	if err != nil || namespace != expected {
		return nil, ErrInvalidLaneIdentity
	}
	fields := v3NamespaceFields{
		ServiceRoot: namespace.ServiceRoot, ConfigFile: namespace.ConfigFile, Database: namespace.Database,
		DatabaseWAL: namespace.DatabaseWAL, DatabaseSHM: namespace.DatabaseSHM,
		APISocket: namespace.APISocket, LocalDSocket: namespace.LocalDSocket, PrivateSocket: namespace.PrivateSocket,
		MailboxRoot: namespace.MailboxRoot, Workspaces: namespace.Workspaces, ScriptTempRoot: namespace.ScriptTempRoot,
		Backups: namespace.Backups, Logs: namespace.Logs, Binaries: namespace.Binaries, Staging: namespace.Staging,
		LifecycleLock: namespace.LifecycleLock, RegistryRoot: namespace.RegistryRoot,
		RunnerBinary: namespace.RunnerBinary, LocalBinary: namespace.LocalBinary,
		LocalDBinary: namespace.LocalDBinary, QueuedSSHBinary: namespace.QueuedSSHBinary,
		SessionAgent: namespace.SessionAgent, LogCapture: namespace.LogCapture,
		LocalAgentLabel: namespace.LocalAgentLabel, LocalDAgentLabel: namespace.LocalDAgentLabel,
		LocalAgentPlist: namespace.LocalAgentPlist, LocalDAgentPlist: namespace.LocalDAgentPlist,
		RunnerUnit: namespace.RunnerUnit, RunnerEntrypoint: namespace.RunnerEntrypoint,
		JournalIdentifier: namespace.JournalIdentifier, QueuedSSHJournal: namespace.QueuedSSHJournal,
		RunnerSlice: namespace.RunnerSlice, QueuedSSHUnit: namespace.QueuedSSHUnit,
		SSHAuthorityPath: namespace.SSHAuthorityPath, QueuePort: namespace.QueuePort, DirectPort: namespace.DirectPort,
		DirectEndpointName: namespace.DirectEndpointName, DirectEndpointURL: namespace.DirectEndpointURL,
		DirectHTTPSBind: namespace.DirectHTTPSBind,
	}
	var document any
	switch namespace.Host {
	case HostKindMac:
		document = struct {
			Version int         `yaml:"version"`
			Lane    ReleaseLane `yaml:"release_lane"`
			Mac     any         `yaml:"mac"`
		}{VersionV3, namespace.Lane, struct {
			Account   string            `yaml:"account"`
			Namespace v3NamespaceFields `yaml:"namespace"`
		}{namespace.Account, fields}}
	case HostKindLinux:
		document = struct {
			Version int         `yaml:"version"`
			Lane    ReleaseLane `yaml:"release_lane"`
			Linux   any         `yaml:"linux"`
		}{VersionV3, namespace.Lane, struct {
			Account       string            `yaml:"account"`
			RemoteProfile string            `yaml:"remote_profile"`
			Namespace     v3NamespaceFields `yaml:"namespace"`
		}{namespace.Account, namespace.RemoteProfile, fields}}
	default:
		return nil, ErrInvalidLaneIdentity
	}
	output, err := yaml.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidLaneIdentity, err)
	}
	return output, nil
}

// ValidateV3NamespaceURL returns true only for the URL derived for this
// namespace, including scheme, address and lane-specific port.
func ValidateV3NamespaceURL(namespace LaneNamespace, value string) bool {
	expected, err := DeriveLaneNamespace(namespace.Host, namespace.Lane, namespace.RemoteProfile)
	if err != nil || namespace != expected {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	return value == expected.DirectEndpointURL
}
