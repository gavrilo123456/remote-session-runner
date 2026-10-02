package runnerlocal

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"remote-session-runner/src/internal/config"
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

func macIngressHealthReportWithMetrics(ctx context.Context, authority *store.AuthorityStore, monitor *routerHealthMonitor, importers []*mailbox.Importer, definitions []config.MailboxDefinition, recorder *opshealth.Recorder, thresholds *opshealth.ThresholdMonitor) opshealth.Report {
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
	mailboxBacklog, readyTotal, err := mailboxBacklogByInbox(ctx, authority, importers)
	if len(importers) == 0 && len(definitions) != 0 {
		mailboxBacklog, readyTotal, err = mailboxBacklogByDefinitions(ctx, authority, definitions)
	}
	if err != nil {
		return report
	}
	if len(mailboxBacklog) != 0 {
		metrics.MailboxBacklogByInbox = mailboxBacklog
		// ReadOperationalMetrics retains the complete durable total. Add only
		// filesystem-ready work; the per-inbox values already include both.
		metrics.MailboxBacklog += readyTotal
	}
	return opshealth.AddMetrics(report, metrics, recorder, thresholds, nil)
}

// mailboxBacklogByDefinitions preserves the doctor view before a service has
// started its runtime. It reads only already-safe mailbox trees and treats a
// missing tree as zero, so it cannot make a candidate mailbox visible.
func mailboxBacklogByDefinitions(ctx context.Context, authority *store.AuthorityStore, definitions []config.MailboxDefinition) (map[string]int64, int64, error) {
	if authority == nil {
		return nil, 0, store.ErrNilDatabase
	}
	ids := make([]string, 0, len(definitions))
	roots := make(map[string]string, len(definitions))
	for _, definition := range definitions {
		if definition.ID == "" || definition.Root == "" {
			return nil, 0, fmt.Errorf("mailbox definition is not configured")
		}
		if _, exists := roots[definition.ID]; exists {
			return nil, 0, fmt.Errorf("mailbox definition ID is duplicated")
		}
		ids = append(ids, definition.ID)
		roots[definition.ID] = definition.Root
	}
	counts, err := authority.CountMailboxBacklogByInbox(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	var readyTotal int64
	for _, mailboxID := range ids {
		classifier, err := mailbox.NewLifecycleClassifier(mailbox.LifecycleClassifierOptions{
			MailboxID: mailboxID, Root: roots[mailboxID], Authority: authority,
		})
		if err != nil {
			return nil, 0, err
		}
		ready, err := classifier.ActionableRequestCount(ctx)
		if err != nil {
			return nil, 0, err
		}
		counts[mailboxID] += ready
		readyTotal += ready
	}
	return counts, readyTotal, nil
}

// mailboxBacklogByInbox combines the authority's durable accepted-exchange
// count with each configured inbox's safe ready-marker count. It returns only
// configured safe IDs, never paths or caller supplied labels.
func mailboxBacklogByInbox(ctx context.Context, authority *store.AuthorityStore, importers []*mailbox.Importer) (map[string]int64, int64, error) {
	if authority == nil {
		return nil, 0, store.ErrNilDatabase
	}
	ids := make([]string, 0, len(importers))
	byID := make(map[string]*mailbox.Importer, len(importers))
	for _, importer := range importers {
		if importer == nil {
			return nil, 0, fmt.Errorf("mailbox importer is not configured")
		}
		mailboxID := importer.MailboxID()
		if mailboxID == "" {
			return nil, 0, fmt.Errorf("mailbox importer has no ID")
		}
		if _, exists := byID[mailboxID]; exists {
			return nil, 0, fmt.Errorf("mailbox importer ID is duplicated")
		}
		byID[mailboxID] = importer
		ids = append(ids, mailboxID)
	}
	counts, err := authority.CountMailboxBacklogByInbox(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	var readyTotal int64
	for _, mailboxID := range ids {
		classifier, err := mailbox.NewLifecycleClassifier(mailbox.LifecycleClassifierOptions{
			MailboxID: mailboxID, Root: filepath.Dir(byID[mailboxID].InboxPath()), Authority: authority,
		})
		if err != nil {
			return nil, 0, err
		}
		ready, err := classifier.ActionableRequestCount(ctx)
		if err != nil {
			return nil, 0, err
		}
		counts[mailboxID] += ready
		readyTotal += ready
	}
	return counts, readyTotal, nil
}
