package runnerd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"remote-session-runner/src/internal/domain"
)

const maxHTTPSCredentialFileBytes = 1 << 20

var (
	ErrDirectHTTPSConfiguration = errors.New("direct HTTPS configuration is invalid")
	ErrPrincipalMap             = errors.New("direct HTTPS principal map is invalid")
)

type directPrincipalContextKey struct{}

// DirectPrincipal is the server-mapped identity of a verified mTLS peer.
// Callers must use this value instead of accepting controller fields from an
// HTTP request body.
type DirectPrincipal struct {
	Controller domain.ControllerIdentity
}

// DirectPrincipalFromContext returns the certificate-derived caller identity
// installed by the direct HTTPS ingress.
func DirectPrincipalFromContext(ctx context.Context) (DirectPrincipal, bool) {
	if ctx == nil {
		return DirectPrincipal{}, false
	}
	principal, ok := ctx.Value(directPrincipalContextKey{}).(DirectPrincipal)
	return principal, ok
}

type DirectHTTPSServerOptions struct {
	BindAddress        string
	ServerCertificate  string
	ServerPrivateKey   string
	ClientCA           string
	ClientPrincipalMap string
	Handler            http.Handler
}

type DirectHTTPSServer struct {
	server   *http.Server
	address  string
	listener net.Listener
	mu       sync.Mutex
	closed   bool
}

type principalMapDocument struct {
	Version    int                         `yaml:"version"`
	Principals []principalMapDocumentEntry `yaml:"principals"`
}

type principalMapDocumentEntry struct {
	URISAN         string `yaml:"uri_san"`
	ControllerType string `yaml:"controller_type"`
	ControllerID   string `yaml:"controller_id"`
}

// NewDirectHTTPSServer loads all server credentials and the explicit client
// identity map before a listener can be opened. The map is rechecked during
// the TLS handshake, before an HTTP handler can run.
func NewDirectHTTPSServer(options DirectHTTPSServerOptions) (*DirectHTTPSServer, error) {
	if options.Handler == nil {
		return nil, fmt.Errorf("%w: HTTP handler is required", ErrDirectHTTPSConfiguration)
	}
	if _, _, err := net.SplitHostPort(options.BindAddress); err != nil {
		return nil, fmt.Errorf("%w: bind address must be host:port", ErrDirectHTTPSConfiguration)
	}
	certificatePEM, err := readOwnerOnlyFile(options.ServerCertificate)
	if err != nil {
		return nil, fmt.Errorf("%w: read server certificate", ErrDirectHTTPSConfiguration)
	}
	keyPEM, err := readOwnerOnlyFile(options.ServerPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("%w: read server private key", ErrDirectHTTPSConfiguration)
	}
	serverCertificate, err := tls.X509KeyPair(certificatePEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("%w: load server certificate and key", ErrDirectHTTPSConfiguration)
	}
	caPEM, err := readOwnerOnlyFile(options.ClientCA)
	if err != nil {
		return nil, fmt.Errorf("%w: read client CA", ErrDirectHTTPSConfiguration)
	}
	clientCAs := x509.NewCertPool()
	if !clientCAs.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("%w: client CA contains no certificates", ErrDirectHTTPSConfiguration)
	}
	principals, err := loadPrincipalMap(options.ClientPrincipalMap)
	if err != nil {
		return nil, err
	}
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{serverCertificate},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientCAs,
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{"http/1.1"},
		VerifyConnection: func(state tls.ConnectionState) error {
			if _, ok := principalForPeer(state.PeerCertificates, principals); !ok {
				return errors.New("client certificate has no mapped principal")
			}
			return nil
		},
	}
	server := &http.Server{
		Handler:           principalHandler(options.Handler, principals),
		TLSConfig:         tlsConfig,
		ReadHeaderTimeout: 5 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	return &DirectHTTPSServer{server: server, address: options.BindAddress}, nil
}

// Listen binds the configured host-local address. The server cannot start
// without a valid server keypair, client CA, and nonempty principal map.
func (s *DirectHTTPSServer) Listen() error {
	if s == nil || s.server == nil {
		return ErrDirectHTTPSConfiguration
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrDirectHTTPSConfiguration
	}
	if s.listener != nil {
		return nil
	}
	listener, err := net.Listen("tcp", s.address)
	if err != nil {
		return fmt.Errorf("listen direct HTTPS on %s: %w", s.address, err)
	}
	s.listener = tls.NewListener(listener, s.server.TLSConfig)
	return nil
}

// Addr reports the configured address before Listen and the bound address
// afterward.
func (s *DirectHTTPSServer) Addr() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return s.address
}

// Serve runs the HTTPS server after Listen.
func (s *DirectHTTPSServer) Serve() error {
	if s == nil || s.server == nil {
		return ErrDirectHTTPSConfiguration
	}
	s.mu.Lock()
	listener := s.listener
	s.mu.Unlock()
	if listener == nil {
		return ErrDirectHTTPSConfiguration
	}
	err := s.server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

// Close gracefully stops the HTTPS listener.
func (s *DirectHTTPSServer) Close(ctx context.Context) error {
	if s == nil || s.server == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	stopErr := s.StopAccepting()
	shutdownErr := s.server.Shutdown(ctx)
	return errors.Join(stopErr, shutdownErr, s.CloseStreams())
}

// StopAccepting closes the HTTPS listener but leaves active handlers and
// followers open for the bounded drain and durable flush stages.
func (s *DirectHTTPSServer) StopAccepting() error {
	if s == nil || s.server == nil {
		return nil
	}
	s.server.SetKeepAlivesEnabled(false)
	s.mu.Lock()
	listener := s.listener
	s.mu.Unlock()
	if listener == nil {
		return nil
	}
	if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		return fmt.Errorf("stop direct HTTPS listener: %w", err)
	}
	return nil
}

// CloseStreams force-closes the remaining HTTP streams after durable state
// has been verified.
func (s *DirectHTTPSServer) CloseStreams() error {
	if s == nil || s.server == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	listener := s.listener
	s.mu.Unlock()
	var closeErr error
	if listener != nil {
		if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			closeErr = err
		}
	}
	if err := s.server.Close(); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
		closeErr = errors.Join(closeErr, err)
	}
	return closeErr
}

func loadPrincipalMap(path string) (map[string]domain.ControllerIdentity, error) {
	contents, err := readOwnerOnlyFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: read configured map", ErrPrincipalMap)
	}
	decoder := yaml.NewDecoder(strings.NewReader(string(contents)))
	decoder.KnownFields(true)
	var document principalMapDocument
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("%w: decode configured map", ErrPrincipalMap)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("%w: map must contain exactly one YAML document", ErrPrincipalMap)
	}
	if document.Version != 1 || len(document.Principals) == 0 {
		return nil, fmt.Errorf("%w: version 1 and at least one principal are required", ErrPrincipalMap)
	}
	principals := make(map[string]domain.ControllerIdentity, len(document.Principals))
	for _, entry := range document.Principals {
		uri, err := url.Parse(entry.URISAN)
		if err != nil || entry.URISAN == "" || uri.Scheme == "" || uri.String() != entry.URISAN {
			return nil, fmt.Errorf("%w: URI SAN must be an absolute URI", ErrPrincipalMap)
		}
		if _, duplicate := principals[entry.URISAN]; duplicate {
			return nil, fmt.Errorf("%w: duplicate URI SAN", ErrPrincipalMap)
		}
		if domain.ControllerType(entry.ControllerType) != domain.ControllerTypeDirectMTLS {
			return nil, fmt.Errorf("%w: only direct_mtls controller identities are allowed", ErrPrincipalMap)
		}
		identity, err := domain.NewControllerIdentity(domain.ControllerType(entry.ControllerType), domain.ControllerID(entry.ControllerID))
		if err != nil {
			return nil, fmt.Errorf("%w: invalid controller identity", ErrPrincipalMap)
		}
		principals[entry.URISAN] = identity
	}
	return principals, nil
}

func readOwnerOnlyFile(path string) ([]byte, error) {
	if path == "" {
		return nil, errors.New("empty file path")
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm()&0o077 != 0 || before.Size() > maxHTTPSCredentialFileBytes {
		return nil, errors.New("file must be a small owner-only regular file")
	}
	stat, ok := before.Sys().(*syscall.Stat_t)
	if !ok || uint32(stat.Uid) != uint32(os.Geteuid()) {
		return nil, errors.New("file owner does not match service user")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || !after.Mode().IsRegular() || after.Size() > maxHTTPSCredentialFileBytes {
		return nil, errors.New("file changed while opening")
	}
	afterOwner, ok := after.Sys().(*syscall.Stat_t)
	if !ok || uint32(afterOwner.Uid) != uint32(os.Geteuid()) || after.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("file owner or mode changed while opening")
	}
	contents, err := io.ReadAll(io.LimitReader(file, maxHTTPSCredentialFileBytes+1))
	if err != nil || len(contents) > maxHTTPSCredentialFileBytes {
		return nil, errors.New("file could not be read within the size limit")
	}
	return contents, nil
}

func principalForPeer(certificates []*x509.Certificate, principals map[string]domain.ControllerIdentity) (DirectPrincipal, bool) {
	if len(certificates) == 0 || len(certificates[0].URIs) != 1 {
		return DirectPrincipal{}, false
	}
	identity, ok := principals[certificates[0].URIs[0].String()]
	if !ok {
		return DirectPrincipal{}, false
	}
	return DirectPrincipal{Controller: identity}, true
}

func principalHandler(next http.Handler, principals map[string]domain.ControllerIdentity) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.TLS == nil {
			http.Error(response, "client certificate required", http.StatusUnauthorized)
			return
		}
		principal, ok := principalForPeer(request.TLS.PeerCertificates, principals)
		if !ok {
			http.Error(response, "client certificate is not mapped", http.StatusForbidden)
			return
		}
		next.ServeHTTP(response, request.WithContext(context.WithValue(request.Context(), directPrincipalContextKey{}, principal)))
	})
}
