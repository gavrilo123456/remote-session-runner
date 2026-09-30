package runnerlocal

import (
	"context"
	"sort"
	"sync"
	"time"

	"remote-session-runner/src/internal/mailbox"
	"remote-session-runner/src/internal/opshealth"
	"remote-session-runner/src/internal/store"
)

const routerHealthMaxAge = 45 * time.Second

type routerHealthSnapshot struct {
	state     opshealth.State
	reason    string
	checkedAt time.Time
}

// routerHealthMonitor retains a separate, safe health snapshot for every
// configured queued remote profile. A successful probe of one profile cannot
// overwrite a failure recorded for another.
type routerHealthMonitor struct {
	mu        sync.RWMutex
	profiles  []string
	snapshots map[string]routerHealthSnapshot
}

// newRouterHealthMonitor retains the original single-profile behavior for
// legacy callers and tests. New multi-profile composition uses the explicit
// profile constructor below.
func newRouterHealthMonitor() *routerHealthMonitor {
	return newRouterHealthMonitorForProfiles([]string{"linux-host"})
}

func newRouterHealthMonitorForProfiles(profiles []string) *routerHealthMonitor {
	unique := make(map[string]struct{}, len(profiles))
	for _, profile := range profiles {
		if profile != "" {
			unique[profile] = struct{}{}
		}
	}
	names := make([]string, 0, len(unique))
	for profile := range unique {
		names = append(names, profile)
	}
	sort.Strings(names)
	monitor := &routerHealthMonitor{profiles: names, snapshots: make(map[string]routerHealthSnapshot, len(names))}
	for _, profile := range names {
		monitor.snapshots[profile] = routerHealthSnapshot{state: opshealth.StateDegraded, reason: "remote_health_not_checked"}
	}
	return monitor
}

// update preserves the legacy one-route test hook.
func (m *routerHealthMonitor) update(err error, now time.Time) {
	if m == nil {
		return
	}
	profiles := m.profileNames()
	if len(profiles) == 0 {
		return
	}
	m.updateProfiles(map[string]error{profiles[0]: err}, now)
}

func (m *routerHealthMonitor) updateProfiles(results map[string]error, now time.Time) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, profile := range m.profiles {
		snapshot := routerHealthSnapshot{state: opshealth.StateReady, checkedAt: now.UTC()}
		err, present := results[profile]
		if !present {
			snapshot.state = opshealth.StateDegraded
			snapshot.reason = "remote_health_not_checked"
		} else if err != nil {
			snapshot.state = opshealth.StateDegraded
			snapshot.reason = "remote_transport_unavailable"
		}
		m.snapshots[profile] = snapshot
	}
}

func (m *routerHealthMonitor) profileNames() []string {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	profiles := append([]string(nil), m.profiles...)
	m.mu.RUnlock()
	return profiles
}

func (m *routerHealthMonitor) current(now time.Time) routerHealthSnapshot {
	profiles := m.profileNames()
	if len(profiles) == 0 {
		return routerHealthSnapshot{state: opshealth.StateReady, reason: "no_queued_remote_profiles"}
	}
	return m.currentProfile(profiles[0], now)
}

func (m *routerHealthMonitor) currentProfile(profile string, now time.Time) routerHealthSnapshot {
	if m == nil {
		return routerHealthSnapshot{state: opshealth.StateNotReady, reason: "router_unavailable"}
	}
	m.mu.RLock()
	snapshot, exists := m.snapshots[profile]
	m.mu.RUnlock()
	if !exists {
		return routerHealthSnapshot{state: opshealth.StateNotReady, reason: "router_unavailable"}
	}
	if snapshot.checkedAt.IsZero() || now.Sub(snapshot.checkedAt) > routerHealthMaxAge {
		return routerHealthSnapshot{state: opshealth.StateDegraded, reason: "remote_health_stale", checkedAt: snapshot.checkedAt}
	}
	return snapshot
}

func macIngressHealthReport(ctx context.Context, authority *store.AuthorityStore, monitor *routerHealthMonitor) opshealth.Report {
	database := opshealth.Check{Component: "sqlite_writes", State: opshealth.StateReady, RequiredForReadiness: true}
	if authority == nil || authority.CheckWritable(ctx, "mac_ingress") != nil {
		database.State = opshealth.StateNotReady
		database.Reason = "database_write_failed"
	}
	backlogDetails := map[string]int64(nil)
	if authority != nil {
		if counts, err := authority.CountRemoteIntentBacklog(ctx); err == nil {
			backlogDetails = map[string]int64{"pending_intents": counts.Pending, "uncertain_intents": counts.Uncertain}
		}
	}
	checks := []opshealth.Check{database}
	checks = append(checks, remoteRouterHealthChecks(monitor, backlogDetails, time.Now())...)
	return opshealth.NewReport("mac_ingress", time.Now(), checks...)
}

func remoteRouterHealthChecks(monitor *routerHealthMonitor, backlogDetails map[string]int64, now time.Time) []opshealth.Check {
	if monitor == nil {
		return []opshealth.Check{{Component: "remote_router", State: opshealth.StateNotReady, Reason: "router_unavailable"}}
	}
	profiles := monitor.profileNames()
	if len(profiles) == 0 {
		return []opshealth.Check{{Component: "remote_router", State: opshealth.StateReady, Reason: "no_queued_remote_profiles", Details: backlogDetails}}
	}
	checks := make([]opshealth.Check, 0, len(profiles))
	for _, profile := range profiles {
		snapshot := monitor.currentProfile(profile, now)
		component := "remote_router"
		if len(profiles) > 1 {
			component += "/" + profile
		}
		checks = append(checks, opshealth.Check{Component: component, State: snapshot.state, Reason: snapshot.reason, Details: backlogDetails})
	}
	return checks
}

func macIngressHealthReportWithMetrics(ctx context.Context, authority *store.AuthorityStore, monitor *routerHealthMonitor, importer *mailbox.Importer, recorder *opshealth.Recorder, thresholds *opshealth.ThresholdMonitor) opshealth.Report {
	report := macIngressHealthReport(ctx, authority, monitor)
	if authority == nil {
		return report
	}
	durable, err := authority.ReadOperationalMetrics(ctx)
	if err != nil {
		return report
	}
	metrics := opshealth.Metrics{
		ActiveSessionSlots: durable.ActiveSessionSlots, ActiveCommandSlots: durable.ActiveCommandSlots,
		QueuedCommands: durable.QueuedCommands, QueuedIntents: durable.QueuedIntents,
		DispatchAttemptsTotal: durable.DispatchAttemptsTotal, ReconciliationAgeSeconds: durable.ReconciliationAgeSeconds,
		EventLagEvents: durable.EventLagEvents, EventGapsTotal: durable.EventGapsTotal,
		OutputTruncationsTotal: durable.OutputTruncationsTotal,
		StorageErrorsTotal:     durable.StorageErrorsTotal, CleanupFailuresTotal: durable.CleanupFailuresTotal,
		MailboxBacklog: durable.MailboxBacklog,
	}
	if importer != nil {
		ready, err := importer.ReadyRequestCount(ctx)
		if err != nil {
			return report
		}
		metrics.MailboxBacklog += ready
	}
	return opshealth.AddMetrics(report, metrics, recorder, thresholds, nil)
}
