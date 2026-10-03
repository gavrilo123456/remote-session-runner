package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const (
	// controlledRestartStatusLegacySchemaVersion is the immediately preceding
	// authority schema. It deliberately remains explicit: a later migration
	// must not accidentally make an older schema look safe to inspect.
	controlledRestartStatusLegacySchemaVersion = 33

	// ControlledRestartStatusLegacy means the authority is at schema 33, before
	// durable controlled-restart plans existed.
	ControlledRestartStatusLegacy ControlledRestartStatus = "legacy"
	// ControlledRestartStatusPrepared means a validated plan exists but has not
	// crossed the scheduler-admission boundary.
	ControlledRestartStatusPrepared ControlledRestartStatus = "prepared"
	// ControlledRestartStatusActive means a validated plan has crossed the
	// scheduler-admission boundary.
	ControlledRestartStatusActive ControlledRestartStatus = "active"
	// ControlledRestartStatusMigratedWithoutPlan means the current schema is
	// present and there is no durable controlled-restart plan.
	ControlledRestartStatusMigratedWithoutPlan ControlledRestartStatus = "migrated-without-plan"
)

// ControlledRestartStatus is the deliberately small public outcome of a
// read-only durable-plan inspection. It exposes no resource identities,
// scripts, payloads, or credential material.
type ControlledRestartStatus string

// ReadControlledRestartStatus verifies the accepted schema shape and returns
// the durable controlled-restart state. It is read-only; callers should open
// the database with OpenExistingControlledRestartStatusReadOnly so the path,
// ownership, sidecar, and immutable-connection requirements are also proved.
func ReadControlledRestartStatus(ctx context.Context, database *sql.DB) (ControlledRestartStatus, error) {
	if database == nil {
		return "", ErrNilDatabase
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return withReadTransaction(ctx, database, func(ctx context.Context, connection *sql.Conn) (ControlledRestartStatus, error) {
		if err := verifyControlledRestartStatusSchemaConnection(ctx, connection); err != nil {
			return "", err
		}
		version, err := userVersion(ctx, connection)
		if err != nil {
			return "", err
		}
		if version == controlledRestartStatusLegacySchemaVersion {
			return ControlledRestartStatusLegacy, nil
		}

		var planCount int
		if err := connection.QueryRowContext(ctx, `SELECT COUNT(*) FROM exec_controlled_restart_plans`).Scan(&planCount); err != nil {
			return "", fmt.Errorf("read controlled restart plan count: %w", err)
		}
		switch planCount {
		case 0:
			var pairCount int
			if err := connection.QueryRowContext(ctx, `SELECT COUNT(*) FROM exec_controlled_restart_plan_lost_pairs`).Scan(&pairCount); err != nil {
				return "", fmt.Errorf("read controlled restart lost-pair count: %w", err)
			}
			if pairCount != 0 {
				return "", ErrControlledRestartPlanCorrupt
			}
			return ControlledRestartStatusMigratedWithoutPlan, nil
		case 1:
			plan, err := readControlledRestartPlanOnConnection(ctx, connection)
			if errors.Is(err, ErrControlledRestartPlanNotFound) {
				return "", ErrControlledRestartPlanCorrupt
			}
			if err != nil {
				return "", err
			}
			if plan.Activated {
				return ControlledRestartStatusActive, nil
			}
			return ControlledRestartStatusPrepared, nil
		default:
			return "", ErrControlledRestartPlanCorrupt
		}
	})
}

// controlledRestartPlanTablesPresent rejects a partially migrated or manually
// altered schema before status code examines any plan rows.
func controlledRestartPlanTablesPresent(ctx context.Context, connection *sql.Conn) (plansPresent, pairsPresent bool, err error) {
	rows, err := connection.QueryContext(ctx, `
SELECT name
FROM sqlite_schema
WHERE type = 'table'
  AND name IN ('exec_controlled_restart_plans', 'exec_controlled_restart_plan_lost_pairs')`)
	if err != nil {
		return false, false, fmt.Errorf("inspect controlled restart plan schema: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, false, fmt.Errorf("read controlled restart plan schema: %w", err)
		}
		switch name {
		case "exec_controlled_restart_plans":
			plansPresent = true
		case "exec_controlled_restart_plan_lost_pairs":
			pairsPresent = true
		default:
			return false, false, ErrSchemaVersion
		}
	}
	if err := rows.Err(); err != nil {
		return false, false, fmt.Errorf("iterate controlled restart plan schema: %w", err)
	}
	return plansPresent, pairsPresent, nil
}
