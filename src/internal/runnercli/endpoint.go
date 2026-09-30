package runnercli

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/runnerclient"
)

var ErrEndpointProfile = errors.New("runner endpoint profile is unavailable or invalid")

const maxCredentialFileBytes = 1 << 20

type sessionClient interface {
	EndpointKind() runnerclient.EndpointKind
	CreateSession(context.Context, runnerclient.CreateSessionRequest, string) (runnerclient.Acceptance, error)
	GetSession(context.Context, string) (runnerclient.Snapshot[runnerclient.SessionResource], error)
}

type endpointResolver interface {
	Resolve(name, configPath string) (sessionClient, error)
	ResolveWithBinding(name, configPath string) (endpointResolution, error)
}

// endpointResolution keeps a client and its direct target binding from the
// same configuration read. The binding is empty for the local Unix endpoint.
// It must not be reconstructed through a second config read after the client
// has been constructed.
type endpointResolution struct {
	client        sessionClient
	targetProfile string
	bound         bool
}

type remoteEndpointProfile struct {
	name          string
	targetProfile string
	endpoint      string
	serverCA      string
	clientCert    string
	clientKey     string
}

func defaultMacConfigPath() string {
	return filepath.Join(config.MacServiceRoot, "config", "mac.yaml")
}

type defaultEndpointResolver struct {
	readRemoteProfile func(name, configPath string) (remoteEndpointProfile, error)
	newHTTPSClient    func(remoteEndpointProfile) (sessionClient, error)
}

func newDefaultEndpointResolver() defaultEndpointResolver {
	return defaultEndpointResolver{
		readRemoteProfile: loadRemoteEndpointProfile,
		newHTTPSClient:    newProfileHTTPSClient,
	}
}

func (r defaultEndpointResolver) Resolve(name, configPath string) (sessionClient, error) {
	resolution, err := r.ResolveWithBinding(name, configPath)
	if err != nil {
		return nil, err
	}
	return resolution.client, nil
}

// ResolveWithBinding constructs the selected endpoint client and returns its
// immutable direct target binding from one validated profile read.
func (r defaultEndpointResolver) ResolveWithBinding(name, configPath string) (endpointResolution, error) {
	if name == "local" {
		socketPath := filepath.Join(config.MacServiceRoot, "run", "local-api.sock")
		client, err := runnerclient.NewUnixSocketClient(socketPath)
		if err != nil {
			return endpointResolution{}, fmt.Errorf("configure local endpoint: %w", err)
		}
		return endpointResolution{client: client}, nil
	}
	if name == "" {
		return endpointResolution{}, errors.New("--endpoint is required; choose local or a configured endpoint profile")
	}
	if r.readRemoteProfile == nil || r.newHTTPSClient == nil {
		return endpointResolution{}, ErrEndpointProfile
	}
	profile, err := r.readRemoteProfile(name, configPath)
	if err != nil {
		return endpointResolution{}, err
	}
	if profile.name != name || profile.targetProfile == "" {
		return endpointResolution{}, ErrEndpointProfile
	}
	client, err := r.newHTTPSClient(profile)
	if err != nil {
		return endpointResolution{}, err
	}
	if client == nil || client.EndpointKind() != runnerclient.EndpointHTTPS {
		return endpointResolution{}, ErrEndpointProfile
	}
	return endpointResolution{client: client, targetProfile: profile.targetProfile, bound: true}, nil
}

// EndpointTargetProfile returns the immutable target binding for a named
// direct endpoint. The local Unix endpoint deliberately has no remote target
// binding.
func (r defaultEndpointResolver) EndpointTargetProfile(name, configPath string) (string, bool, error) {
	if name == "local" {
		return "", false, nil
	}
	if name == "" || r.readRemoteProfile == nil {
		return "", false, ErrEndpointProfile
	}
	profile, err := r.readRemoteProfile(name, configPath)
	if err != nil || profile.name != name || profile.targetProfile == "" {
		return "", false, ErrEndpointProfile
	}
	return profile.targetProfile, true, nil
}

func loadRemoteEndpointProfile(name, configPath string) (remoteEndpointProfile, error) {
	loaded, err := config.LoadFile(configPath)
	if err != nil || loaded.Kind() != config.HostKindMac {
		return remoteEndpointProfile{}, ErrEndpointProfile
	}
	endpoint, ok := loaded.DirectEndpoint(name)
	if !ok || endpoint.Name != name || endpoint.TargetProfile == "" || endpoint.Endpoint == "" || endpoint.ServerCA == "" ||
		endpoint.ClientCertificate == "" || endpoint.ClientPrivateKeyFile == "" {
		return remoteEndpointProfile{}, ErrEndpointProfile
	}
	return remoteEndpointProfile{
		name: name, targetProfile: endpoint.TargetProfile, endpoint: endpoint.Endpoint, serverCA: endpoint.ServerCA,
		clientCert: endpoint.ClientCertificate, clientKey: endpoint.ClientPrivateKeyFile,
	}, nil
}

func remoteProfileFromSettings(name string, kind config.HostKind, settings config.MacSettings, key config.SecretReference, settingsOK, keyOK bool) (remoteEndpointProfile, error) {
	if !settingsOK || !keyOK || kind != config.HostKindMac || name == "" || name != settings.RemoteEndpointProfile || settings.RemoteEndpoint == "" ||
		settings.RemoteServerCA == "" || settings.DirectClientCertificate == "" || key.File == "" {
		return remoteEndpointProfile{}, ErrEndpointProfile
	}
	return remoteEndpointProfile{
		name: name, targetProfile: "linux-host", endpoint: settings.RemoteEndpoint, serverCA: settings.RemoteServerCA,
		clientCert: settings.DirectClientCertificate, clientKey: key.File,
	}, nil
}

func newProfileHTTPSClient(profile remoteEndpointProfile) (sessionClient, error) {
	caPEM, err := readOwnerOnlyCredential(profile.serverCA)
	if err != nil {
		return nil, fmt.Errorf("%w: trusted server CA file is unavailable or unsafe", ErrEndpointProfile)
	}
	certificatePEM, err := readOwnerOnlyCredential(profile.clientCert)
	if err != nil {
		return nil, fmt.Errorf("%w: client certificate file is unavailable or unsafe", ErrEndpointProfile)
	}
	privateKeyPEM, err := readOwnerOnlyCredential(profile.clientKey)
	if err != nil {
		return nil, fmt.Errorf("%w: client private-key file is unavailable or unsafe", ErrEndpointProfile)
	}

	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("%w: trusted server CA file contains no usable certificate", ErrEndpointProfile)
	}
	certificate, err := tls.X509KeyPair(certificatePEM, privateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("%w: client certificate and private key do not form a usable pair", ErrEndpointProfile)
	}
	client, err := runnerclient.NewHTTPSClient(runnerclient.HTTPSConfig{
		Endpoint:          profile.endpoint,
		RootCAs:           roots,
		ClientCertificate: certificate,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: HTTPS client configuration is invalid", ErrEndpointProfile)
	}
	return client, nil
}

func readOwnerOnlyCredential(path string) ([]byte, error) {
	if path == "" || !filepath.IsAbs(path) {
		return nil, ErrEndpointProfile
	}
	linkInfo, err := os.Lstat(path)
	if err != nil || !linkInfo.Mode().IsRegular() || linkInfo.Mode().Perm()&0o077 != 0 || linkInfo.Mode().Perm()&0o400 == 0 {
		return nil, ErrEndpointProfile
	}
	if err := validateCredentialOwner(linkInfo, uint32(os.Geteuid())); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, ErrEndpointProfile
	}
	defer file.Close()
	fileInfo, err := file.Stat()
	if err != nil || !fileInfo.Mode().IsRegular() || !os.SameFile(linkInfo, fileInfo) {
		return nil, ErrEndpointProfile
	}
	if err := validateCredentialOwner(fileInfo, uint32(os.Geteuid())); err != nil {
		return nil, err
	}
	if fileInfo.Mode().Perm()&0o077 != 0 || fileInfo.Mode().Perm()&0o400 == 0 || fileInfo.Size() < 0 || fileInfo.Size() > maxCredentialFileBytes {
		return nil, ErrEndpointProfile
	}
	contents, err := io.ReadAll(io.LimitReader(file, maxCredentialFileBytes+1))
	if err != nil || len(contents) > maxCredentialFileBytes {
		return nil, ErrEndpointProfile
	}
	return contents, nil
}

func validateCredentialOwner(info os.FileInfo, expectedUID uint32) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint32(stat.Uid) != expectedUID {
		return ErrEndpointProfile
	}
	return nil
}
