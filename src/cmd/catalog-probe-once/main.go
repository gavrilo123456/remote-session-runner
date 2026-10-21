package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/laneinstall"
	"remote-session-runner/src/internal/sshbridge"
	"remote-session-runner/src/internal/sshclient"
)

type prepared struct {
	HostID         string          `json:"host_id"`
	Transport      string          `json:"transport"`
	OperationID    string          `json:"operation_id"`
	ControllerID   string          `json:"controller_id"`
	IdempotencyKey string          `json:"idempotency_key"`
	RequestSHA256  string          `json:"request_body_sha256"`
	RequestBody    json.RawMessage `json:"request_body"`
	Token          string          `json:"one_time_token"`
}

type outcome struct {
	Transport           string `json:"transport"`
	HostID              string `json:"host_id"`
	OperationID         string `json:"operation_id"`
	AcceptedAt          string `json:"accepted_at"`
	JobID               string `json:"job_id"`
	SessionID           string `json:"session_id"`
	CommandID           string `json:"command_id"`
	Phase               string `json:"phase"`
	CommandState        string `json:"command_state"`
	ExitCode            *int   `json:"exit_code,omitempty"`
	FinalEventSequence  *int64 `json:"final_event_sequence,omitempty"`
	OutputComplete      bool   `json:"output_complete"`
	OutputTruncated     bool   `json:"output_truncated"`
	TeardownState       string `json:"teardown_state"`
	HTTPStatus          int    `json:"http_status,omitempty"`
	BridgeResponseType  string `json:"bridge_response_type,omitempty"`
	ReadinessHTTPStatus int    `json:"readiness_http_status"`
}

type jobFields struct {
	JobID              string `json:"job_id"`
	SessionID          string `json:"session_id"`
	CommandID          string `json:"command_id"`
	Phase              string `json:"phase"`
	JobPhase           string `json:"job_phase"`
	CommandState       string `json:"command_state"`
	ExitCode           *int   `json:"exit_code"`
	FinalEventSequence *int64 `json:"final_event_sequence"`
	OutputComplete     bool   `json:"output_complete"`
	OutputTruncated    bool   `json:"output_truncated"`
	TeardownState      string `json:"teardown_state"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	mode := flag.String("mode", "", "direct, queued, or local")
	host := flag.String("host", "", "canonical worker host for a negative direct fence check")
	configPath := flag.String("config", "", "validated Mac lane config")
	socketPath := flag.String("socket", "", "Mac-local API Unix socket")
	preparedPath := flag.String("prepared", "", "owner-only catalog-probe-prepare output")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if *mode == "fence-direct" || *mode == "fence-local" {
		if *preparedPath != "" {
			return errors.New("fence check does not accept a prepared probe capability")
		}
		if *mode == "fence-direct" {
			if *host != "linux-host" && *host != "sandbox-host" && *host != "factory-sandbox" {
				return errors.New("direct fence host is invalid")
			}
			return directFence(ctx, *configPath, *host)
		}
		if *host != "" {
			return errors.New("Mac-local fence does not accept a worker host")
		}
		return localFence(ctx, *socketPath)
	}
	if *preparedPath == "" || (*mode != "direct" && *mode != "queued" && *mode != "local") {
		return errors.New("invalid probe helper arguments")
	}
	raw, err := os.ReadFile(*preparedPath)
	if err != nil {
		return errors.New("prepared capability is unavailable")
	}
	var p prepared
	if json.Unmarshal(raw, &p) != nil || p.HostID == "" || p.OperationID == "" || p.Token == "" || p.IdempotencyKey == "" || len(p.RequestBody) == 0 {
		return errors.New("prepared capability is invalid")
	}
	if p.Transport != map[string]string{"direct": "direct-mtls", "queued": "queued-ssh", "local": "mac-local"}[*mode] {
		return errors.New("prepared capability transport mismatch")
	}
	preparedBody, err := compactPreparedRequestBody(p.RequestBody, p.RequestSHA256)
	if err != nil {
		return errors.New("prepared request body digest is invalid")
	}
	p.RequestBody = preparedBody
	switch *mode {
	case "direct":
		return direct(ctx, *configPath, p)
	case "queued":
		return queued(ctx, *configPath, p)
	default:
		return local(ctx, *socketPath, p)
	}
}

func compactPreparedRequestBody(body json.RawMessage, expectedSHA256 string) (json.RawMessage, error) {
	if len(body) == 0 || expectedSHA256 == "" {
		return nil, errors.New("prepared request body is incomplete")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, body); err != nil {
		return nil, err
	}
	digest := sha256.Sum256(compact.Bytes())
	if hex.EncodeToString(digest[:]) != expectedSHA256 {
		return nil, errors.New("prepared request body digest mismatch")
	}
	return append(json.RawMessage(nil), compact.Bytes()...), nil
}

func directFence(ctx context.Context, configPath, host string) error {
	loaded, err := config.LoadFile(configPath)
	if err != nil {
		return errors.New("Mac lane config could not be validated")
	}
	remote, ok := loaded.RemoteHost(host)
	if !ok || remote.DirectEndpoint == nil {
		return errors.New("configured direct route is unavailable")
	}
	d := remote.DirectEndpoint
	caPEM, err := os.ReadFile(d.ServerCA)
	if err != nil {
		return errors.New("configured server CA is unavailable")
	}
	clientCertPEM, err := os.ReadFile(d.ClientCertificate)
	if err != nil {
		return errors.New("configured client certificate is unavailable")
	}
	clientKeyPEM, err := os.ReadFile(d.ClientPrivateKeyFile)
	if err != nil {
		return errors.New("configured client key is unavailable")
	}
	tlsConfig, err := validatedTLSConfig(d.Endpoint, d.ServerCAFingerprint, d.ExpectedServerFingerprint, d.ServerURISAN,
		d.ClientCertFingerprint, d.ClientSPKIFingerprint, d.ClientURISAN, caPEM, clientCertPEM, clientKeyPEM)
	if err != nil {
		return errors.New("configured direct TLS material failed certificate, CA, fingerprint, or URI validation")
	}
	client := &http.Client{Timeout: 12 * time.Second, Transport: &http.Transport{Proxy: nil, TLSClientConfig: tlsConfig, ForceAttemptHTTP2: true}}
	ready, err := request(ctx, client, http.MethodGet, d.Endpoint+"/health/ready", nil, nil)
	if err != nil || ready.status != http.StatusOK {
		return fmt.Errorf("direct fence readiness failed: HTTP %d", ready.status)
	}
	blocked, err := request(ctx, client, http.MethodPost, d.Endpoint+"/v1/jobs", []byte("{}"), nil)
	if err != nil {
		return errors.New("direct fence request transport failed")
	}
	return reportFence("direct-mtls", host, ready.status, blocked.status, blocked.body)
}

func localFence(ctx context.Context, socketPath string) error {
	if socketPath == "" || !filepath.IsAbs(socketPath) || filepath.Clean(socketPath) != socketPath {
		return errors.New("Mac-local API socket is invalid")
	}
	transport := &http.Transport{Proxy: nil}
	transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", socketPath)
	}
	client := &http.Client{Timeout: 12 * time.Second, Transport: transport}
	ready, err := request(ctx, client, http.MethodGet, "http://runner/health/ready", nil, nil)
	if err != nil || ready.status != http.StatusOK {
		return fmt.Errorf("Mac-local fence readiness failed: HTTP %d", ready.status)
	}
	blocked, err := request(ctx, client, http.MethodPost, "http://runner/v1/jobs", []byte("{}"), nil)
	if err != nil {
		return errors.New("Mac-local fence request transport failed")
	}
	return reportFence("mac-local", "mac-control-plane", ready.status, blocked.status, blocked.body)
}

func reportFence(transport, host string, readinessStatus, status int, body []byte) error {
	var envelope struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(body, &envelope)
	if status != http.StatusServiceUnavailable || envelope.Code != "maintenance_admission_closed" {
		return fmt.Errorf("%s admission fence not confirmed: readiness HTTP %d, POST HTTP %d, response_code %q", transport, readinessStatus, status, envelope.Code)
	}
	return printOutcome(outcome{Transport: transport, HostID: host, Phase: "blocked", HTTPStatus: status, ReadinessHTTPStatus: readinessStatus})
}

func direct(ctx context.Context, configPath string, p prepared) error {
	loaded, err := config.LoadFile(configPath)
	if err != nil {
		return errors.New("Mac lane config could not be validated")
	}
	remote, ok := loaded.RemoteHost(p.HostID)
	if !ok || remote.DirectEndpoint == nil {
		return errors.New("configured direct route is unavailable")
	}
	d := remote.DirectEndpoint
	caPEM, err := os.ReadFile(d.ServerCA)
	if err != nil {
		return errors.New("configured server CA is unavailable")
	}
	clientCertPEM, err := os.ReadFile(d.ClientCertificate)
	if err != nil {
		return errors.New("configured client certificate is unavailable")
	}
	clientKeyPEM, err := os.ReadFile(d.ClientPrivateKeyFile)
	if err != nil {
		return errors.New("configured client key is unavailable")
	}
	tlsConfig, err := validatedTLSConfig(d.Endpoint, d.ServerCAFingerprint, d.ExpectedServerFingerprint, d.ServerURISAN,
		d.ClientCertFingerprint, d.ClientSPKIFingerprint, d.ClientURISAN, caPEM, clientCertPEM, clientKeyPEM)
	if err != nil {
		return errors.New("configured direct TLS material failed certificate, CA, fingerprint, or URI validation")
	}
	client := &http.Client{Timeout: 12 * time.Second, Transport: &http.Transport{Proxy: nil, TLSClientConfig: tlsConfig, ForceAttemptHTTP2: true}}
	ready, err := request(ctx, client, http.MethodGet, d.Endpoint+"/health/ready", nil, nil)
	if err != nil || ready.status != http.StatusOK {
		return fmt.Errorf("direct readiness failed: HTTP %d", ready.status)
	}
	headers := make(http.Header)
	headers.Set("Idempotency-Key", p.IdempotencyKey)
	// The direct HTTPS handler retains its established maintenance header;
	// local API and SSH bridge transports use the laneinstall header constant.
	headers.Set("X-Runner-Maintenance-Probe", p.Token)
	accepted, err := request(ctx, client, http.MethodPost, d.Endpoint+"/v1/jobs", p.RequestBody, headers)
	if err != nil || accepted.status != http.StatusAccepted {
		return fmt.Errorf("direct probe was not accepted: HTTP %d, response_code %q", accepted.status, responseErrorCode(accepted.body))
	}
	var acceptance struct {
		JobID     string `json:"job_id"`
		SessionID string `json:"session_id"`
		CommandID string `json:"command_id"`
	}
	if json.Unmarshal(accepted.body, &acceptance) != nil || acceptance.JobID == "" || acceptance.SessionID == "" || acceptance.CommandID == "" {
		return errors.New("direct probe acceptance lacked stable identities")
	}
	statusURL := d.Endpoint + "/v1/jobs/" + acceptance.JobID
	var final jobFields
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		read, readErr := request(ctx, client, http.MethodGet, statusURL, nil, nil)
		if readErr != nil || read.status != http.StatusOK || parseJob(read.body, &final) != nil {
			return errors.New("direct probe status read failed")
		}
		if terminal(final) {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !terminal(final) {
		return errors.New("direct probe did not reach a terminal state within the bound")
	}
	return printOutcome(outcome{Transport: p.Transport, HostID: p.HostID, OperationID: p.OperationID, AcceptedAt: time.Now().UTC().Format(time.RFC3339Nano), JobID: acceptance.JobID, SessionID: acceptance.SessionID, CommandID: acceptance.CommandID, Phase: finalPhase(final), CommandState: final.CommandState, ExitCode: final.ExitCode, FinalEventSequence: final.FinalEventSequence, OutputComplete: final.OutputComplete, OutputTruncated: final.OutputTruncated, TeardownState: final.TeardownState, HTTPStatus: accepted.status, ReadinessHTTPStatus: ready.status})
}

func validatedTLSConfig(endpoint, caFingerprint, serverFingerprint, serverURI, clientFingerprint, clientSPKI, clientURI string,
	caPEM, clientCertPEM, clientKeyPEM []byte) (*tls.Config, error) {
	parsedURL, err := url.Parse(endpoint)
	if err != nil || parsedURL.Scheme != "https" || parsedURL.Host == "" || parsedURL.User != nil ||
		parsedURL.Path != "" && parsedURL.Path != "/" || parsedURL.RawQuery != "" || parsedURL.Fragment != "" {
		return nil, errors.New("invalid direct endpoint")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) || !pemFingerprintPresent(caPEM, caFingerprint) {
		return nil, errors.New("invalid direct trust root")
	}
	pair, err := tls.X509KeyPair(clientCertPEM, clientKeyPEM)
	if err != nil || len(pair.Certificate) == 0 {
		return nil, errors.New("invalid client key pair")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || certificateFingerprint(leaf) != clientFingerprint || spkiFingerprint(leaf) != clientSPKI ||
		!hasURI(leaf, clientURI) || time.Now().Before(leaf.NotBefore) || time.Now().After(leaf.NotAfter) {
		return nil, errors.New("client certificate mismatch")
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS13, ServerName: parsedURL.Hostname(), RootCAs: roots, Certificates: []tls.Certificate{pair},
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 || len(state.VerifiedChains) == 0 || certificateFingerprint(state.PeerCertificates[0]) != serverFingerprint ||
				!hasURI(state.PeerCertificates[0], serverURI) || !verifiedChainEndsAt(state.VerifiedChains, caFingerprint) {
				return errors.New("server identity mismatch")
			}
			return nil
		},
	}, nil
}

func certificateFingerprint(certificate *x509.Certificate) string {
	if certificate == nil {
		return ""
	}
	digest := sha256.Sum256(certificate.Raw)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func spkiFingerprint(certificate *x509.Certificate) string {
	if certificate == nil {
		return ""
	}
	digest := sha256.Sum256(certificate.RawSubjectPublicKeyInfo)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func hasURI(certificate *x509.Certificate, expected string) bool {
	if certificate == nil || expected == "" {
		return false
	}
	for _, uri := range certificate.URIs {
		if uri.String() == expected {
			return true
		}
	}
	return false
}

func pemFingerprintPresent(contents []byte, expected string) bool {
	for len(contents) > 0 {
		block, rest := pem.Decode(contents)
		if block == nil {
			return false
		}
		contents = rest
		if block.Type != "CERTIFICATE" {
			continue
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err == nil && certificateFingerprint(certificate) == expected {
			return true
		}
	}
	return false
}

func verifiedChainEndsAt(chains [][]*x509.Certificate, expected string) bool {
	for _, chain := range chains {
		if len(chain) > 0 && certificateFingerprint(chain[len(chain)-1]) == expected {
			return true
		}
	}
	return false
}

func queued(ctx context.Context, configPath string, p prepared) error {
	loaded, err := config.LoadFile(configPath)
	if err != nil {
		return errors.New("Mac lane config could not be validated")
	}
	remote, ok := loaded.RemoteHost(p.HostID)
	if !ok || remote.QueuedBridge == nil {
		return errors.New("configured queued route is unavailable")
	}
	client, err := sshclient.New(sshclient.Config{User: remote.Account, Host: remote.QueuedBridge.Host, Port: remote.QueuedBridge.Port,
		IdentityFile: remote.QueuedBridge.PrivateKeyFile, KnownHostsFile: remote.QueuedBridge.KnownHostsFile,
		HostKeyFingerprint: remote.QueuedBridge.HostKeyFingerprint, ConnectTimeout: 10 * time.Second})
	if err != nil {
		return errors.New("configured pinned SSH bridge identity failed validation")
	}
	if reply, callErr := client.Call(ctx, sshbridge.RequestFrame{ProtocolVersion: sshbridge.ProtocolVersion, RequestID: "catalog-probe-ping-" + p.HostID,
		Operation: sshbridge.OperationPing, Payload: json.RawMessage(`{}`)}); callErr != nil || reply.ResponseType != "result" {
		return errors.New("pinned queued SSH bridge preflight failed")
	}
	jobID, err := randomID("job")
	if err != nil {
		return errors.New("could not allocate one-off probe identity")
	}
	sessionID, err := randomID("sess")
	if err != nil {
		return errors.New("could not allocate one-off probe identity")
	}
	commandID, err := randomID("cmd")
	if err != nil {
		return errors.New("could not allocate one-off probe identity")
	}
	var payload map[string]json.RawMessage
	if json.Unmarshal(p.RequestBody, &payload) != nil {
		return errors.New("queued probe body is invalid")
	}
	payload["session_id"], _ = json.Marshal(sessionID)
	payload["command_id"], _ = json.Marshal(commandID)
	body, err := json.Marshal(payload)
	if err != nil {
		return errors.New("queued probe body could not be prepared")
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeQueuedMac, domain.ControllerID(p.ControllerID))
	if err != nil {
		return errors.New("queued controller identity is invalid")
	}
	_ = controller
	requestID := "catalog-probe-" + p.OperationID + "-" + p.HostID + "-queued"
	run := sshbridge.RequestFrame{ProtocolVersion: sshbridge.ProtocolVersion, RequestID: requestID,
		Operation: sshbridge.OperationRunOrResumeJob, ResourceID: jobID, IdempotencyKey: p.IdempotencyKey,
		MaintenanceProbeToken: p.Token, Payload: body}
	accepted, err := client.Call(ctx, run)
	if err != nil || accepted.ResponseType != "result" {
		return errors.New("queued probe call failed before a verifiable acceptance")
	}
	var acceptFields jobFields
	if json.Unmarshal(accepted.Payload, &acceptFields) != nil || acceptFields.JobID != jobID || acceptFields.SessionID != sessionID || acceptFields.CommandID != commandID {
		return errors.New("queued probe acknowledgement identities did not match")
	}
	var final jobFields
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		payload, _ := json.Marshal(map[string]string{"job_id": jobID})
		read, readErr := client.Call(ctx, sshbridge.RequestFrame{ProtocolVersion: sshbridge.ProtocolVersion, RequestID: requestID + "-status",
			Operation: sshbridge.OperationGetJob, MaintenanceProbeToken: p.Token, Payload: payload})
		if readErr != nil || read.ResponseType != "result" || parseJob(read.Payload, &final) != nil {
			return errors.New("queued probe status read failed")
		}
		if terminal(final) {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !terminal(final) {
		return errors.New("queued probe did not reach a terminal state within the bound")
	}
	return printOutcome(outcome{Transport: p.Transport, HostID: p.HostID, OperationID: p.OperationID, AcceptedAt: time.Now().UTC().Format(time.RFC3339Nano), JobID: jobID, SessionID: sessionID, CommandID: commandID, Phase: finalPhase(final), CommandState: final.CommandState, ExitCode: final.ExitCode, FinalEventSequence: final.FinalEventSequence, OutputComplete: final.OutputComplete, OutputTruncated: final.OutputTruncated, TeardownState: final.TeardownState, BridgeResponseType: accepted.ResponseType})
}

func local(ctx context.Context, socketPath string, p prepared) error {
	if socketPath == "" || !filepath.IsAbs(socketPath) || filepath.Clean(socketPath) != socketPath {
		return errors.New("Mac-local API socket is invalid")
	}
	transport := &http.Transport{Proxy: nil}
	transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", socketPath)
	}
	client := &http.Client{Timeout: 12 * time.Second, Transport: transport}
	ready, err := request(ctx, client, http.MethodGet, "http://runner/health/ready", nil, nil)
	if err != nil || ready.status != http.StatusOK {
		return fmt.Errorf("Mac-local readiness failed: HTTP %d", ready.status)
	}
	headers := make(http.Header)
	headers.Set("Idempotency-Key", p.IdempotencyKey)
	headers.Set(laneinstall.CatalogMaintenanceProbeTokenHeader, p.Token)
	accepted, err := request(ctx, client, http.MethodPost, "http://runner/v1/jobs", p.RequestBody, headers)
	if err != nil || accepted.status != http.StatusAccepted {
		return fmt.Errorf("Mac-local probe was not accepted: HTTP %d, response_code %q", accepted.status, responseErrorCode(accepted.body))
	}
	var acceptance struct {
		JobID     string `json:"job_id"`
		SessionID string `json:"session_id"`
		CommandID string `json:"command_id"`
	}
	if json.Unmarshal(accepted.body, &acceptance) != nil || acceptance.JobID == "" || acceptance.SessionID == "" || acceptance.CommandID == "" {
		return errors.New("Mac-local probe acceptance lacked stable identities")
	}
	var final jobFields
	statusURL := "http://runner/v1/jobs/" + acceptance.JobID
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		read, readErr := request(ctx, client, http.MethodGet, statusURL, nil, nil)
		if readErr != nil || read.status != http.StatusOK || parseJob(read.body, &final) != nil {
			return errors.New("Mac-local probe status read failed")
		}
		if terminal(final) {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !terminal(final) {
		return errors.New("Mac-local probe did not reach a terminal state within the bound")
	}
	return printOutcome(outcome{Transport: p.Transport, HostID: p.HostID, OperationID: p.OperationID, AcceptedAt: time.Now().UTC().Format(time.RFC3339Nano), JobID: acceptance.JobID, SessionID: acceptance.SessionID, CommandID: acceptance.CommandID, Phase: finalPhase(final), CommandState: final.CommandState, ExitCode: final.ExitCode, FinalEventSequence: final.FinalEventSequence, OutputComplete: final.OutputComplete, OutputTruncated: final.OutputTruncated, TeardownState: final.TeardownState, HTTPStatus: accepted.status, ReadinessHTTPStatus: ready.status})
}

type response struct {
	status int
	body   []byte
}

func request(ctx context.Context, client *http.Client, method, url string, body []byte, headers http.Header) (response, error) {
	var input io.Reader
	if body != nil {
		input = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, input)
	if err != nil {
		return response{}, errors.New("request could not be constructed")
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, values := range headers {
		for _, v := range values {
			req.Header.Add(k, v)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return response{}, errors.New("request transport failed")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return response{}, errors.New("bounded response read failed")
	}
	return response{status: resp.StatusCode, body: data}, nil
}

func responseErrorCode(body []byte) string {
	var envelope struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(body, &envelope) != nil || envelope.Code == "" || len(envelope.Code) > 80 {
		return "unclassified"
	}
	for _, r := range envelope.Code {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return "unclassified"
		}
	}
	return envelope.Code
}

func parseJob(raw []byte, out *jobFields) error {
	var envelope struct {
		Resource json.RawMessage `json:"resource"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return errors.New("invalid job status")
	}
	if len(envelope.Resource) != 0 {
		if json.Unmarshal(envelope.Resource, out) != nil {
			return errors.New("invalid job resource")
		}
	} else if json.Unmarshal(raw, out) != nil {
		return errors.New("invalid job status")
	}
	if out.Phase == "" {
		out.Phase = out.JobPhase
	}
	return nil
}

func terminal(j jobFields) bool {
	return j.Phase == "complete" || j.Phase == "failed" || j.Phase == "lost"
}
func finalPhase(j jobFields) string {
	if j.Phase != "" {
		return j.Phase
	}
	return j.JobPhase
}

func randomID(prefix string) (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return prefix + "-" + hex.EncodeToString(value[:]), nil
}
func printOutcome(value outcome) error {
	data, err := json.Marshal(value)
	if err != nil {
		return errors.New("probe result could not be encoded")
	}
	_, err = os.Stdout.Write(append(data, '\n'))
	return err
}
