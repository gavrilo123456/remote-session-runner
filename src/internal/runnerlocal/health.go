package runnerlocal

import (
	"context"
	"sync"
	"time"

	"remote-session-runner/src/internal/opshealth"
	"remote-session-runner/src/internal/store"
)

const routerHealthMaxAge = 45 * time.Second

type routerHealthSnapshot struct {
	state     opshealth.State
	reason    string
	checkedAt time.Time
}

type routerHealthMonitor struct {
	mu       sync.RWMutex
	snapshot routerHealthSnapshot
}

func newRouterHealthMonitor() *routerHealthMonitor {
	return &routerHealthMonitor{snapshot: routerHealthSnapshot{
		state:  opshealth.StateDegraded,
		reason: "remote_health_not_checked",
	}}
}

func (m *routerHealthMonitor) update(err error, now time.Time) {
	if m == nil {
		return
	}
	snapshot := routerHealthSnapshot{state: opshealth.StateReady, checkedAt: now.UTC()}
	if err != nil {
		snapshot.state = opshealth.StateDegraded
		snapshot.reason = "remote_transport_unavailable"
	}
	m.mu.Lock()
	m.snapshot = snapshot
	m.mu.Unlock()
}

func (m *routerHealthMonitor) current(now time.Time) routerHealthSnapshot {
	if m == nil {
		return routerHealthSnapshot{state: opshealth.StateNotReady, reason: "router_unavailable"}
	}
	m.mu.RLock()
	snapshot := m.snapshot
	m.mu.RUnlock()
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
	backlog := opshealth.Check{Component: "remote_router", State: opshealth.StateDegraded, Reason: "remote_health_not_checked"}
	if counts, err := authority.CountRemoteIntentBacklog(ctx); err == nil {
		backlog.Details = map[string]int64{"pending_intents": counts.Pending, "uncertain_intents": counts.Uncertain}
	} else {
		backlog.Reason = "router_status_unavailable"
	}
	if monitor != nil {
		snapshot := monitor.current(time.Now())
		backlog.State = snapshot.state
		backlog.Reason = snapshot.reason
	}
	return opshealth.NewReport("mac_ingress", time.Now(), database, backlog)
}
