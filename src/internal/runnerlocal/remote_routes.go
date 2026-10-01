package runnerlocal

import (
	"errors"
	"fmt"
	"net"
	"strconv"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/dispatcher"
	"remote-session-runner/src/internal/sshclient"
)

// queuedBridgeEndpoints returns display-only route labels for safe operational
// diagnostics. The map contains no credential paths or key material.
func queuedBridgeEndpoints(loaded config.Config) map[string]string {
	endpoints := make(map[string]string)
	for _, profile := range loaded.RemoteHostNames() {
		host, ok := loaded.RemoteHost(profile)
		if !ok || host.QueuedBridge == nil {
			continue
		}
		endpoints[profile] = net.JoinHostPort(host.QueuedBridge.Host, strconv.Itoa(host.QueuedBridge.Port))
	}
	return endpoints
}

type sshBridgeClientFactory func(sshclient.Config) (dispatcher.RemoteCaller, error)

// newPinnedRemoteCallerResolver builds one restricted SSH caller for each
// queued route in the immutable Mac configuration. Direct-only remote hosts
// intentionally have no Router caller: they are available only to the direct
// mTLS CLI path.
func newPinnedRemoteCallerResolver(loaded config.Config) (dispatcher.RemoteCallerResolver, error) {
	return newPinnedRemoteCallerResolverWithFactory(loaded, func(settings sshclient.Config) (dispatcher.RemoteCaller, error) {
		return sshclient.New(settings)
	})
}

func newPinnedRemoteCallerResolverWithFactory(loaded config.Config, newCaller sshBridgeClientFactory) (dispatcher.RemoteCallerResolver, error) {
	if loaded.Kind() != config.HostKindMac || newCaller == nil {
		return nil, errors.New("selected Mac remote route configuration is required")
	}
	callers := make(map[string]dispatcher.RemoteCaller)
	for _, profile := range loaded.RemoteHostNames() {
		host, exists := loaded.RemoteHost(profile)
		if !exists || host.Profile != profile || host.Account != config.LinuxAccount {
			return nil, errors.New("configured remote host route is invalid")
		}
		if host.QueuedBridge == nil {
			continue
		}
		caller, err := newCaller(sshclient.Config{
			User:           host.Account,
			Host:           host.QueuedBridge.Host,
			Port:           host.QueuedBridge.Port,
			IdentityFile:   host.QueuedBridge.PrivateKeyFile,
			KnownHostsFile: host.QueuedBridge.KnownHostsFile,
		})
		if err != nil {
			return nil, fmt.Errorf("construct pinned SSH bridge client for profile %q: %w", profile, err)
		}
		if caller == nil {
			return nil, errors.New("configured remote bridge caller is unavailable")
		}
		callers[profile] = caller
	}
	return dispatcher.NewRemoteCallerResolver(callers)
}
