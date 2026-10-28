package releaseflow

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/laneinstall"
)

// Capacity belongs to the host that owns the backup destination. In particular,
// never stat a worker's /home/ubuntu path on the Mac coordinator, even if a path
// with that name happens to exist there.
func (b *NativePromotionBackend) readPromotionCapacity(ctx context.Context, host, lane string, snapshot *promotionLaneSnapshot) error {
	if b != nil && b.capacityReader != nil {
		return b.capacityReader(ctx, host, lane, snapshot)
	}
	if b == nil || snapshot == nil || !contains(HostOrder, host) || !contains(LaneOrder, lane) || !shaPattern.MatchString(b.ControllerRevision) {
		return ErrInvalidInput
	}
	profile, ok := b.Config.Hosts[host]
	if !ok || profile.HostID != host {
		return ErrInvalidInput
	}
	var report HostCapacityReport
	if host == "mac-control-plane" {
		var err error
		report, err = RunHostCapacity(ctx, host, lane, profile.Repository, b.ControllerRevision)
		if err != nil {
			return err
		}
	} else {
		transport := NativeBuildBackend{}
		args := []string{"host-capacity", "--host", host, "--lane", lane,
			"--repository", profile.Repository, "--controller-revision", b.ControllerRevision}
		out, err := transport.remote(ctx, profile, args, nil)
		if err != nil {
			return err
		}
		if err := decodeStrictJSON(out, &report); err != nil {
			return fmt.Errorf("%s %s capacity response: %w", host, lane, ErrOperationCorrupted)
		}
	}
	return applyPromotionCapacityReport(report, host, lane, b.ControllerRevision, b.OperatingBuffer, time.Now(), snapshot)
}

func applyPromotionCapacityReport(report HostCapacityReport, host, lane, controller string, buffer uint64, now time.Time, snapshot *promotionLaneSnapshot) error {
	plan, err := laneinstall.BuildCatalogPlan(host, config.ReleaseLane(lane))
	if err != nil || snapshot == nil || !shaPattern.MatchString(controller) || report.HostID != host || report.Lane != lane ||
		report.ControllerRevision != controller || report.BackupPath != plan.Namespace.Backups {
		return fmt.Errorf("%s %s capacity identity mismatch: %w", host, lane, ErrOperationCorrupted)
	}
	measuredPath := report.MeasuredPath
	if measuredPath == "" { // Accept the original report format for existing operation records.
		measuredPath = report.BackupPath
	}
	if snapshot.NamespaceAbsence != nil {
		if host != "linux-host" || lane != "prd" || measuredPath != filepath.Dir(plan.Namespace.ServiceRoot) ||
			report.NamespaceAbsenceEvidenceSHA256 != snapshot.NamespaceAbsence.EvidenceSHA256 {
			return fmt.Errorf("%s replacement PRD capacity is not bound to the selected namespace absence: %w", host, ErrOperationCorrupted)
		}
	} else if measuredPath != plan.Namespace.Backups || report.NamespaceAbsenceEvidenceSHA256 != "" {
		return fmt.Errorf("%s %s capacity measured outside the existing backup root without absence evidence: %w", host, lane, ErrOperationCorrupted)
	}
	observed, err := time.Parse(time.RFC3339Nano, report.ObservedAtUTC)
	if err != nil || observed.IsZero() || observed.Before(now.Add(-2*time.Minute)) || observed.After(now.Add(2*time.Minute)) {
		return fmt.Errorf("%s %s capacity observation is not fresh: %w", host, lane, ErrOperationCorrupted)
	}
	snapshot.AvailableBytes = report.AvailableBytes
	snapshot.RequiredBytes = promotionRequiredCapacity(lane, buffer)
	return nil
}
