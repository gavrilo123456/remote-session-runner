package laneinstall

import (
	"fmt"

	"remote-session-runner/src/internal/config"
)

type Component struct {
	Name     string
	Identity string
}

type AssetPolicy struct {
	Name             string
	Path             string
	OwnerComponent   string
	ReaderComponents []string
	Mode             string
	ReadBoundary     string
	PreserveOnUpdate bool
}

type SocketPolicy struct {
	Name          string
	Path          string
	PeerComponent string
	PeerIdentity  string
}

type ProtectedStatePolicy struct {
	AuthorityStatePreserved        bool
	AuthorityPublicAnchorPreserved bool
	SecurityAnchorStatePreserved   bool
	LaneArtifactsMayReplaceMgmt    bool
}

type HostManagementPackage struct {
	HostID     string
	Components []Component
	Assets     []AssetPolicy
	Sockets    []SocketPolicy
	Protected  ProtectedStatePolicy
}

// ManagementPackage returns the fixed host role and sensitive-resource
// boundaries from design section 7. It is descriptive input to later
// installers; it does not create accounts, keys, sockets, directories, or
// services.
func ManagementPackage(hostID string) (HostManagementPackage, error) {
	tuple, ok := canonicalHosts[hostID]
	if !ok {
		return HostManagementPackage{}, ErrInvalidHost
	}
	roles := []Component{
		{Name: "runner-release-builder", Identity: "unprivileged sealed-staging builder"},
	}
	if tuple.kind == config.HostKindMac {
		roles = append(roles,
			Component{Name: "runner-release-transport", Identity: "root launchd release transport"},
			Component{Name: "runner-security-anchor-helper", Identity: "root protected host helper"},
			Component{Name: "runner-release-authority-signer", Identity: "root launchd signer"},
			Component{Name: "runner-release-authority-agent", Identity: "root launchd authority agent"},
			Component{Name: "runner-release-authority-approval", Identity: "interactive owner approval tool"},
			Component{Name: "runner-release-transport-pki-issuer", Identity: "Mac-only release-transport CA issuer"},
			Component{Name: "runner-release-transport-pki-installer", Identity: "root constrained local-key installer"},
		)
	} else {
		roles = append(roles,
			Component{Name: "runner-release-transport", Identity: "dedicated non-login runner-release-transport account"},
			Component{Name: "runner-security-anchor-helper", Identity: "root protected host helper"},
			Component{Name: "runner-release-transport-pki-installer", Identity: "root constrained local-key installer"},
		)
	}

	anchorRoot := "/var/lib/remote-session-runner-security-anchor"
	if tuple.kind == config.HostKindMac {
		anchorRoot = "/Library/Application Support/RemoteSessionRunner-security-anchor"
	}
	assets := []AssetPolicy{
		{Name: "host-attestation-private-key", OwnerComponent: "runner-security-anchor-helper", ReaderComponents: []string{"runner-security-anchor-helper"}, Mode: "0600", PreserveOnUpdate: true},
		{Name: "security-anchor-state", Path: anchorRoot, OwnerComponent: "runner-security-anchor-helper", ReaderComponents: []string{"runner-security-anchor-helper"}, Mode: "0700", PreserveOnUpdate: true},
	}
	runnerPeer := "ubuntu"
	if tuple.kind == config.HostKindMac {
		runnerPeer = "tomasz.walczuk"
	}
	sockets := []SocketPolicy{
		{Name: "security-anchor-observe", Path: "/var/run/remote-session-runner/security-anchor.sock", PeerComponent: "runner-service", PeerIdentity: runnerPeer},
		{Name: "security-anchor-release-transport", Path: "/var/run/remote-session-runner/security-anchor-release-transport.sock", PeerComponent: "runner-release-transport", PeerIdentity: "measured transport identity"},
	}
	if tuple.kind == config.HostKindMac {
		assets = append(assets,
			AssetPolicy{Name: "release-authority-private-key", Path: "/Library/Application Support/RemoteSessionRunner-release-authority-signer/release-authority.ed25519", OwnerComponent: "runner-release-authority-signer", ReaderComponents: []string{"runner-release-authority-signer"}, Mode: "0600", PreserveOnUpdate: true},
			AssetPolicy{Name: "release-authority-state", Path: "/Library/Application Support/RemoteSessionRunner-release-authority-signer/state", OwnerComponent: "runner-release-authority-signer", ReaderComponents: []string{"runner-release-authority-signer"}, Mode: "0700", PreserveOnUpdate: true},
			AssetPolicy{Name: "release-transport-ca-private-key", OwnerComponent: "runner-release-transport-pki-issuer", ReaderComponents: []string{"runner-release-transport-pki-issuer"}, Mode: "0600", PreserveOnUpdate: true},
			AssetPolicy{Name: "release-transport-client-leaf-private-key", OwnerComponent: "runner-release-transport-pki-installer", ReaderComponents: []string{"runner-release-authority-agent"}, ReadBoundary: "authority-agent-code-identity-only", PreserveOnUpdate: true},
		)
		sockets = append(sockets,
			SocketPolicy{Name: "security-anchor-authority-state", Path: "/var/run/remote-session-runner/security-anchor-authority-state.sock", PeerComponent: "runner-release-authority-agent", PeerIdentity: "launchd audit token, label, and code identity"},
			SocketPolicy{Name: "release-transport-local", Path: "/var/run/remote-session-runner/release-transport.sock", PeerComponent: "runner-release-authority-agent", PeerIdentity: "launchd audit token, label, and code identity"},
			SocketPolicy{Name: "release-authority-signing", PeerComponent: "runner-release-authority-agent", PeerIdentity: "launchd audit token, label, and code identity"},
		)
	} else {
		assets = append(assets,
			AssetPolicy{Name: "release-transport-server-leaf-private-key", OwnerComponent: "runner-release-transport", ReaderComponents: []string{"runner-release-transport"}, Mode: "0600", PreserveOnUpdate: true},
		)
		sockets = append(sockets,
			SocketPolicy{Name: "release-transport-mtls-listener", Path: "tcp:9443", PeerComponent: "runner-release-authority-agent", PeerIdentity: "verified release-authority client URI and active-peer set"},
		)
	}
	return HostManagementPackage{
		HostID: hostID, Components: roles, Assets: assets, Sockets: sockets,
		Protected: ProtectedStatePolicy{
			AuthorityStatePreserved:        tuple.kind == config.HostKindMac,
			AuthorityPublicAnchorPreserved: true,
			SecurityAnchorStatePreserved:   true,
			LaneArtifactsMayReplaceMgmt:    false,
		},
	}, nil
}

// ValidateManagementPackage checks the closed host matrix and key/socket
// boundaries before a package is consumed by later installation code.
func ValidateManagementPackage(pkg HostManagementPackage) error {
	expected, err := ManagementPackage(pkg.HostID)
	if err != nil {
		return err
	}
	if len(expected.Components) != len(pkg.Components) || len(expected.Assets) != len(pkg.Assets) || len(expected.Sockets) != len(pkg.Sockets) ||
		pkg.Protected != expected.Protected {
		return fmt.Errorf("management package does not match fixed host matrix")
	}
	for i := range expected.Components {
		if expected.Components[i] != pkg.Components[i] {
			return fmt.Errorf("management component order or identity differs from fixed matrix")
		}
	}
	for i := range expected.Assets {
		want, got := expected.Assets[i], pkg.Assets[i]
		if want.Name != got.Name || want.Path != got.Path || want.OwnerComponent != got.OwnerComponent || want.Mode != got.Mode || want.ReadBoundary != got.ReadBoundary || want.PreserveOnUpdate != got.PreserveOnUpdate || !equalStrings(want.ReaderComponents, got.ReaderComponents) {
			return fmt.Errorf("sensitive asset boundary differs from fixed policy")
		}
	}
	for i := range expected.Sockets {
		if expected.Sockets[i] != pkg.Sockets[i] {
			return fmt.Errorf("socket peer differs from fixed policy")
		}
	}
	return nil
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
