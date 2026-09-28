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
}

type remoteEndpointProfile struct {
	name       string
	endpoint   string
	serverCA   string
	clientCert string
	clientKey  string
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
	if name == "local" {
		socketPath := filepath.Join(config.MacServiceRoot, "run", "local-api.sock")
		client, err := runnerclient.NewUnixSocketClient(socketPath)
		if err != nil {
			return nil, fmt.Errorf("configure local endpoint: %w", err)
		}
		return client, nil
	}
	if name == "" {
		return nil, errors.New("--endpoint is required; choose local or a configured endpoint profile")
	}
	if r.readRemoteProfile == nil || r.newHTTPSClient == nil {
		return nil, ErrEndpointProfile
	}
	profile, err := r.readRemoteProfile(name, configPath)
	if err != nil {
		return nil, err
	}
	if profile.name != name {
		return nil, ErrEndpointProfile
	}
	client, err := r.newHTTPSClient(profile)
	if err != nil {
		return nil, err
	}
	if client == nil || client.EndpointKind() != runnerclient.EndpointHTTPS {
		return nil, ErrEndpointProfile
	}
	return client, nil
}

func loadRemoteEndpointProfile(name, configPath string) (remoteEndpointProfile, error) {
	loaded, err := config.LoadFile(configPath)
	if err != nil || loaded.Kind() != config.HostKindMac {
		return remoteEndpointProfile{}, ErrEndpointProfile
	}
	mac, macOK := loaded.MacSettings()
	key, keyOK := loaded.SecretReference(config.SecretDirectClientTLSKey)
	return remoteProfileFromSettings(name, loaded.Kind(), mac, key, macOK, keyOK)
}

func remoteProfileFromSettings(name string, kind config.HostKind, settings config.MacSettings, key config.SecretReference, settingsOK, keyOK bool) (remoteEndpointProfile, error) {
	if !settingsOK || !keyOK || kind != config.HostKindMac || name == "" || name != settings.RemoteEndpointProfile || settings.RemoteEndpoint == "" ||
		settings.RemoteServerCA == "" || settings.DirectClientCertificate == "" || key.File == "" {
		return remoteEndpointProfile{}, ErrEndpointProfile
	}
	return remoteEndpointProfile{
		name: name, endpoint: settings.RemoteEndpoint, serverCA: settings.RemoteServerCA,
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
