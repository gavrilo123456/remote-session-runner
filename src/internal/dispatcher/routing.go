package dispatcher

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"remote-session-runner/src/internal/domain"
)

// ErrRemoteRouteUnavailable means that the immutable target profile recorded
// on a remote intent has no configured queued route. Callers must not retry a
// different profile when this error is returned.
var ErrRemoteRouteUnavailable = errors.New("remote target route is unavailable")

// RemoteCallerResolver resolves an immutable remote target profile to its
// pinned bridge caller. A resolver must never provide a fallback caller for a
// different profile.
type RemoteCallerResolver interface {
	ResolveRemoteCaller(profile string) (RemoteCaller, error)
	RemoteCallerProfiles() []string
}

type remoteCallerResolver struct {
	callers  map[string]RemoteCaller
	profiles []string
}

// NewRemoteCallerResolver copies the supplied profile map and provides only
// exact profile lookups. An empty map is valid for a Mac configuration that
// has direct-only remote endpoints and no queued bridge routes.
func NewRemoteCallerResolver(callers map[string]RemoteCaller) (RemoteCallerResolver, error) {
	resolver := &remoteCallerResolver{callers: make(map[string]RemoteCaller, len(callers))}
	for profile, caller := range callers {
		if strings.TrimSpace(profile) == "" || profile != strings.TrimSpace(profile) || caller == nil {
			return nil, ErrRemoteDriverConfiguration
		}
		if _, exists := resolver.callers[profile]; exists {
			return nil, ErrRemoteDriverConfiguration
		}
		resolver.callers[profile] = caller
		resolver.profiles = append(resolver.profiles, profile)
	}
	sort.Strings(resolver.profiles)
	return resolver, nil
}

func (r *remoteCallerResolver) ResolveRemoteCaller(profile string) (RemoteCaller, error) {
	if r == nil || strings.TrimSpace(profile) == "" {
		return nil, ErrRemoteRouteUnavailable
	}
	caller, exists := r.callers[profile]
	if !exists || caller == nil {
		return nil, fmt.Errorf("%w: profile %q", ErrRemoteRouteUnavailable, profile)
	}
	return caller, nil
}

func (r *remoteCallerResolver) RemoteCallerProfiles() []string {
	if r == nil {
		return nil
	}
	return append([]string(nil), r.profiles...)
}

func (d *RemoteDriver) configured() bool {
	return d != nil && d.authority != nil && d.resolver != nil
}

func (d *RemoteDriver) remoteCallerForTarget(target domain.ExecutionTarget) (RemoteCaller, error) {
	if d == nil || d.resolver == nil {
		return nil, ErrRemoteDriverConfiguration
	}
	if target.Kind() != domain.TargetKindRemote || target.Profile() == "" {
		return nil, fmt.Errorf("%w: target is not a configured remote profile", ErrRemoteRouteUnavailable)
	}
	caller, err := d.resolver.ResolveRemoteCaller(target.Profile())
	if err != nil {
		return nil, err
	}
	return caller, nil
}

func (d *RemoteDriver) remoteCallerForProfile(profile string) (RemoteCaller, error) {
	if d == nil || d.resolver == nil {
		return nil, ErrRemoteDriverConfiguration
	}
	return d.resolver.ResolveRemoteCaller(profile)
}

func (d *RemoteDriver) remoteCallerProfiles() []string {
	if d == nil || d.resolver == nil {
		return nil
	}
	return d.resolver.RemoteCallerProfiles()
}

// RemoteProfiles returns the configured queued target profiles in stable
// order. Direct-only profiles are not included because they have no SSH
// bridge caller.
func (d *RemoteDriver) RemoteProfiles() []string { return d.remoteCallerProfiles() }

func sameRemoteTarget(left, right domain.ExecutionTarget) bool {
	return left.Kind() == domain.TargetKindRemote && right.Kind() == domain.TargetKindRemote &&
		left.Profile() == right.Profile()
}
