package laneinstall

import (
	"context"
	"reflect"
	"time"
)

// Hold discovery is independent of evidence age. Permission to advance this
// held successor is not: expired evidence requires the exact stored renewal,
// its four-host receipt bindings, and a fresh local operational observation.
func verifyCatalogCohortSupersessionWindow(ctx context.Context, plan Plan, successor CatalogCohortReceipt,
	at time.Time) (*CatalogCohortContinuationRenewal, error) {
	return verifyCatalogCohortSupersessionWindowWithReaders(ctx, plan, successor, at,
		VerifyCatalogCohortContinuationRenewal, ReadCatalogCohortReceipt)
}

func verifyCatalogCohortSupersessionWindowWithReaders(ctx context.Context, plan Plan, successor CatalogCohortReceipt,
	at time.Time, verify func(context.Context, Plan, string) (CatalogCohortContinuationRenewal, error),
	read func(Plan, string) (CatalogCohortReceipt, error)) (*CatalogCohortContinuationRenewal, error) {
	if validateCatalogPlan(plan) != nil || ValidateCatalogCohortReceipt(successor) != nil ||
		plan.Lane != successor.Lane || successor.Stage != CatalogCohortHeld || at.IsZero() {
		return nil, ErrCatalogOperationInvalid
	}
	if cohortEvidenceFresh(successor, at.UTC().Format(time.RFC3339Nano)) {
		return nil, nil
	}
	renewal, err := verify(ctx, plan, successor.OperationID)
	if err != nil || ValidateCatalogCohortContinuationRenewalForReceipt(renewal, successor, at) != nil {
		return nil, ErrCatalogOperationInvalid
	}
	current, err := read(plan, successor.OperationID)
	if err != nil || !reflect.DeepEqual(current, successor) {
		return nil, ErrCatalogOperationInvalid
	}
	return &renewal, nil
}
