package laneinstall

import (
	"reflect"
	"time"

	"remote-session-runner/src/internal/config"
)

// A fully rolled-back predecessor and its fresh, fenced successor represent
// one closed admission hold during supported supersession. This recognizes
// the rollback baseline, including an unselected replacement namespace; it
// neither authorizes work nor observes or changes any host state.
func catalogCohortRolledBackReplacementHoldPair(predecessor, successor CatalogCohortReceipt, now time.Time) bool {
	if ValidateCatalogCohortReceipt(predecessor) != nil || ValidateCatalogCohortReceipt(successor) != nil ||
		predecessor.Lane != config.ReleaseLanePRD || successor.Lane != config.ReleaseLanePRD ||
		predecessor.OperationID == successor.OperationID || predecessor.Stage != CatalogCohortRolledBack ||
		successor.Stage != CatalogCohortHeld || !catalogCohortReceiptSupersessionEligible(predecessor) ||
		!cohortEvidenceFresh(successor, now.UTC().Format(time.RFC3339Nano)) ||
		(currentSupersessionSuccessor(predecessor.Transitions) != "" &&
			currentSupersessionSuccessor(predecessor.Transitions) != successor.OperationID) {
		return false
	}
	for index, hostID := range catalogCohortSwitchOrder {
		oldHost, nextHost := predecessor.Hosts[index], successor.Hosts[index]
		if oldHost.HostID != hostID || nextHost.HostID != hostID || nextHost.ServiceActiveAtPreflight ||
			nextHost.PreviousTarget != oldHost.PreviousTarget || nextHost.PreviousBundleSHA256 != oldHost.PreviousBundleSHA256 ||
			nextHost.PreviousReadSchemaMax != oldHost.PreviousReadSchemaMax ||
			nextHost.ConfigurationSHA256 != oldHost.ConfigurationSHA256 || nextHost.SchemaVersion != oldHost.SchemaVersion ||
			!oldHost.WorkInventoryEmpty || !nextHost.WorkInventoryEmpty || nextHost.WorkInventorySHA256 != oldHost.WorkInventorySHA256 ||
			!reflect.DeepEqual(nextHost.FreshInstall, oldHost.FreshInstall) || !reflect.DeepEqual(nextHost.LegacyRollback, oldHost.LegacyRollback) {
			return false
		}
		// Receipt validation binds every PRD fence to its host's exact selector,
		// configuration, schema, work identity and stopped-service observation.
		fence, found := catalogCohortHostFenceFor(successor, hostID)
		if !found || !fence.ServiceStopped {
			return false
		}
	}
	return true
}
