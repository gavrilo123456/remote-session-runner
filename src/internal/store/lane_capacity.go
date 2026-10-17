package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"regexp"
	"time"
)

var (
	ErrLaneCapacityInvalid             = errors.New("invalid lane capacity reservation")
	ErrLaneCapacityUnconfigured        = errors.New("lane capacity budget is not configured")
	ErrLaneCapacityExceeded            = errors.New("lane capacity budget exceeded")
	ErrLaneCapacityReservationConflict = errors.New("lane capacity reservation conflicts with retained reservation")
	ErrLaneCapacityReservationNotFound = errors.New("lane capacity reservation was not found")
	laneCapacityDigestPattern          = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	laneCapacityReservationIDPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	laneCapacityCategoryPattern        = regexp.MustCompile(`^(ready_pair|response|event|output|sqlite|wal|log|workspace|temp_script|backup|staging)$`)
)

// LaneByteAllocation identifies one independently retained category charged
// to a lane's durable byte budget.
type LaneByteAllocation struct {
	Category string
	Bytes    uint64
}

// LaneByteBudgetSnapshot is the durable budget and its currently retained use.
type LaneByteBudgetSnapshot struct {
	Lane          string
	MaxBytes      uint64
	ReservedBytes uint64
	PolicyDigest  string
}

// SetLaneByteBudget records the validated policy's byte ceiling. A policy
// cannot shrink below durable reservations already owned by the lane.
func (s *AuthorityStore) SetLaneByteBudget(ctx context.Context, lane string, maxBytes uint64, policyDigest string) error {
	if !validCapacityLane(lane) || maxBytes == 0 || maxBytes > math.MaxInt64 || !laneCapacityDigestPattern.MatchString(policyDigest) {
		return ErrLaneCapacityInvalid
	}
	_, err := withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) (struct{}, error) {
		used, err := sumLaneCapacityReservations(ctx, connection, lane)
		if err != nil {
			return struct{}{}, err
		}
		if used > maxBytes {
			return struct{}{}, ErrLaneCapacityExceeded
		}
		_, err = connection.ExecContext(ctx, `INSERT INTO lane_capacity_budgets(lane, max_reserved_bytes, policy_digest, updated_at)
			VALUES(?, ?, ?, ?) ON CONFLICT(lane) DO UPDATE SET max_reserved_bytes=excluded.max_reserved_bytes,
			policy_digest=excluded.policy_digest, updated_at=excluded.updated_at`, lane, int64(maxBytes), policyDigest, s.now().UTC().Format(time.RFC3339Nano))
		if err != nil {
			return struct{}{}, fmt.Errorf("write durable lane capacity budget: %w", err)
		}
		return struct{}{}, nil
	})
	return err
}

// AdmitLaneBytes inserts durable byte reservations and invokes the caller's
// acceptance mutation under the same BEGIN IMMEDIATE transaction. A failed
// callback rolls both the reservations and its mutation back. The callback
// still performs the caller's usual request/idempotency lookup before it
// creates work; repeating a reservation does not make arbitrary mutations
// idempotent.
func (s *AuthorityStore) AdmitLaneBytes(ctx context.Context, lane, reservationID string, allocations []LaneByteAllocation, mutate func(context.Context, *sql.Conn) error) error {
	if !validCapacityLane(lane) || !laneCapacityReservationIDPattern.MatchString(reservationID) || len(allocations) == 0 || mutate == nil {
		return ErrLaneCapacityInvalid
	}
	seen := make(map[string]struct{}, len(allocations))
	for _, allocation := range allocations {
		if !laneCapacityCategoryPattern.MatchString(allocation.Category) || allocation.Bytes == 0 || allocation.Bytes > math.MaxInt64 {
			return ErrLaneCapacityInvalid
		}
		if _, duplicate := seen[allocation.Category]; duplicate {
			return ErrLaneCapacityInvalid
		}
		seen[allocation.Category] = struct{}{}
	}
	_, err := withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) (struct{}, error) {
		var maxBytes int64
		var policyDigest string
		if err := connection.QueryRowContext(ctx, `SELECT max_reserved_bytes, policy_digest FROM lane_capacity_budgets WHERE lane = ?`, lane).Scan(&maxBytes, &policyDigest); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return struct{}{}, ErrLaneCapacityUnconfigured
			}
			return struct{}{}, fmt.Errorf("read durable lane capacity budget: %w", err)
		}
		if maxBytes <= 0 || !laneCapacityDigestPattern.MatchString(policyDigest) {
			return struct{}{}, ErrLaneCapacityInvalid
		}
		used, err := sumLaneCapacityReservations(ctx, connection, lane)
		if err != nil {
			return struct{}{}, err
		}
		existing, err := laneReservationCategories(ctx, connection, lane, reservationID)
		if err != nil {
			return struct{}{}, err
		}
		if len(existing) > 0 && len(existing) != len(allocations) {
			return struct{}{}, ErrLaneCapacityReservationConflict
		}
		var additional uint64
		for _, allocation := range allocations {
			if retained, found := existing[allocation.Category]; found {
				if retained != allocation.Bytes {
					return struct{}{}, ErrLaneCapacityReservationConflict
				}
				continue
			}
			if len(existing) > 0 {
				return struct{}{}, ErrLaneCapacityReservationConflict
			}
			if allocation.Bytes > math.MaxInt64-used-additional {
				return struct{}{}, ErrLaneCapacityExceeded
			}
			additional += allocation.Bytes
		}
		if used > uint64(maxBytes) || additional > uint64(maxBytes)-used {
			return struct{}{}, ErrLaneCapacityExceeded
		}
		now := s.now().UTC().Format(time.RFC3339Nano)
		for _, allocation := range allocations {
			_, err := connection.ExecContext(ctx, `INSERT INTO lane_capacity_reservations(lane, reservation_id, category, reserved_bytes, created_at)
				VALUES(?, ?, ?, ?, ?) ON CONFLICT(lane, reservation_id, category) DO NOTHING`, lane, reservationID, allocation.Category, int64(allocation.Bytes), now)
			if err != nil {
				return struct{}{}, fmt.Errorf("persist lane capacity reservation: %w", err)
			}
		}
		if err := mutate(ctx, connection); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, nil
	})
	return err
}

// ReleaseLaneBytes runs cleanup and deletes every category for the exact
// reservation in one transaction. Cleanup failure preserves the reservation.
func (s *AuthorityStore) ReleaseLaneBytes(ctx context.Context, lane, reservationID string, cleanup func(context.Context, *sql.Conn) error) error {
	if !validCapacityLane(lane) || !laneCapacityReservationIDPattern.MatchString(reservationID) || cleanup == nil {
		return ErrLaneCapacityInvalid
	}
	_, err := withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) (struct{}, error) {
		var exists int
		if err := connection.QueryRowContext(ctx, `SELECT 1 FROM lane_capacity_reservations WHERE lane = ? AND reservation_id = ? LIMIT 1`, lane, reservationID).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return struct{}{}, ErrLaneCapacityReservationNotFound
			}
			return struct{}{}, fmt.Errorf("read lane capacity reservation before release: %w", err)
		}
		if err := cleanup(ctx, connection); err != nil {
			return struct{}{}, err
		}
		if _, err := connection.ExecContext(ctx, `DELETE FROM lane_capacity_reservations WHERE lane = ? AND reservation_id = ?`, lane, reservationID); err != nil {
			return struct{}{}, fmt.Errorf("delete durable lane capacity reservation: %w", err)
		}
		return struct{}{}, nil
	})
	return err
}

// LaneByteCapacitySnapshot reads the durable lane ceiling and retained use.
func (s *AuthorityStore) LaneByteCapacitySnapshot(ctx context.Context, lane string) (LaneByteBudgetSnapshot, error) {
	if s == nil || s.db == nil || !validCapacityLane(lane) {
		return LaneByteBudgetSnapshot{}, ErrLaneCapacityInvalid
	}
	return withReadTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) (LaneByteBudgetSnapshot, error) {
		var snapshot LaneByteBudgetSnapshot
		snapshot.Lane = lane
		var maximum int64
		if err := connection.QueryRowContext(ctx, `SELECT max_reserved_bytes, policy_digest FROM lane_capacity_budgets WHERE lane = ?`, lane).Scan(&maximum, &snapshot.PolicyDigest); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return LaneByteBudgetSnapshot{}, ErrLaneCapacityUnconfigured
			}
			return LaneByteBudgetSnapshot{}, fmt.Errorf("read lane capacity snapshot: %w", err)
		}
		if maximum <= 0 || !laneCapacityDigestPattern.MatchString(snapshot.PolicyDigest) {
			return LaneByteBudgetSnapshot{}, ErrLaneCapacityInvalid
		}
		snapshot.MaxBytes = uint64(maximum)
		used, err := sumLaneCapacityReservations(ctx, connection, lane)
		if err != nil {
			return LaneByteBudgetSnapshot{}, err
		}
		snapshot.ReservedBytes = used
		if used > snapshot.MaxBytes {
			return LaneByteBudgetSnapshot{}, ErrLaneCapacityExceeded
		}
		return snapshot, nil
	})
}

func sumLaneCapacityReservations(ctx context.Context, connection *sql.Conn, lane string) (uint64, error) {
	rows, err := connection.QueryContext(ctx, `SELECT reservation_id, category, reserved_bytes FROM lane_capacity_reservations WHERE lane = ?`, lane)
	if err != nil {
		return 0, fmt.Errorf("list durable lane capacity reservations: %w", err)
	}
	defer rows.Close()
	var total uint64
	for rows.Next() {
		var reservationID, category string
		var value int64
		if err := rows.Scan(&reservationID, &category, &value); err != nil {
			return 0, fmt.Errorf("read durable lane capacity reservation: %w", err)
		}
		if !laneCapacityReservationIDPattern.MatchString(reservationID) || !laneCapacityCategoryPattern.MatchString(category) || value <= 0 {
			return 0, ErrLaneCapacityInvalid
		}
		if total > math.MaxInt64 || uint64(value) > math.MaxInt64-total {
			return 0, ErrLaneCapacityExceeded
		}
		total += uint64(value)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate durable lane capacity reservations: %w", err)
	}
	return total, nil
}

func laneReservationCategories(ctx context.Context, connection *sql.Conn, lane, reservationID string) (map[string]uint64, error) {
	rows, err := connection.QueryContext(ctx, `SELECT category, reserved_bytes FROM lane_capacity_reservations WHERE lane = ? AND reservation_id = ?`, lane, reservationID)
	if err != nil {
		return nil, fmt.Errorf("list durable reservation categories: %w", err)
	}
	defer rows.Close()
	categories := make(map[string]uint64)
	for rows.Next() {
		var category string
		var bytes int64
		if err := rows.Scan(&category, &bytes); err != nil {
			return nil, fmt.Errorf("read durable reservation category: %w", err)
		}
		if !laneCapacityCategoryPattern.MatchString(category) || bytes <= 0 {
			return nil, ErrLaneCapacityInvalid
		}
		if _, duplicate := categories[category]; duplicate {
			return nil, ErrLaneCapacityInvalid
		}
		categories[category] = uint64(bytes)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate durable reservation categories: %w", err)
	}
	return categories, nil
}

func validCapacityLane(lane string) bool { return lane == "dev" || lane == "qas" || lane == "prd" }
