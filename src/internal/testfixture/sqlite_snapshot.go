package testfixture

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// SQLiteQuery describes one deterministic, read-only query in a captured
// logical database snapshot. Callers should include ORDER BY for row sets.
type SQLiteQuery struct {
	Name string
	SQL  string
	Args []any
}

// SQLiteSnapshot is a consistent logical view captured in one read
// transaction. It deliberately records selected rows rather than copying the
// main database file, which would omit committed WAL content.
type SQLiteSnapshot struct {
	Version int                   `json:"version"`
	Queries []SQLiteQuerySnapshot `json:"queries"`
}

// SQLiteQuerySnapshot contains the columns and rows returned by one query.
type SQLiteQuerySnapshot struct {
	Name    string         `json:"name"`
	Columns []string       `json:"columns"`
	Rows    [][]SQLiteCell `json:"rows"`
}

// SQLiteCell preserves the SQLite value type in the JSON snapshot.
type SQLiteCell struct {
	Type  string `json:"type"`
	Value string `json:"value,omitempty"`
}

// CaptureSQLiteSnapshot runs named SELECT queries in one query-only
// transaction and returns canonicalized result rows. Queries must be a single
// SELECT statement; their ORDER BY clauses define row ordering.
func CaptureSQLiteSnapshot(ctx context.Context, db *sql.DB, queries ...SQLiteQuery) (SQLiteSnapshot, error) {
	if db == nil {
		return SQLiteSnapshot{}, errors.New("SQLite snapshot requires a database")
	}
	if len(queries) == 0 {
		return SQLiteSnapshot{}, errors.New("SQLite snapshot requires at least one query")
	}
	for _, query := range queries {
		if strings.TrimSpace(query.Name) == "" {
			return SQLiteSnapshot{}, errors.New("SQLite snapshot query requires a name")
		}
		normalized := strings.ToLower(strings.Join(strings.Fields(query.SQL), " "))
		if !strings.HasPrefix(normalized, "select ") || strings.Contains(query.SQL, ";") {
			return SQLiteSnapshot{}, fmt.Errorf("SQLite snapshot query %q must be one SELECT statement", query.Name)
		}
	}

	connection, err := db.Conn(ctx)
	if err != nil {
		return SQLiteSnapshot{}, fmt.Errorf("reserve SQLite snapshot connection: %w", err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, "PRAGMA query_only = ON"); err != nil {
		return SQLiteSnapshot{}, fmt.Errorf("enable SQLite query-only mode: %w", err)
	}
	defer func() {
		_, _ = connection.ExecContext(context.Background(), "PRAGMA query_only = OFF")
	}()
	transaction, err := connection.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SQLiteSnapshot{}, fmt.Errorf("begin SQLite snapshot transaction: %w", err)
	}
	defer transaction.Rollback()

	snapshot := SQLiteSnapshot{Version: 1, Queries: make([]SQLiteQuerySnapshot, 0, len(queries))}
	for _, query := range queries {
		querySnapshot, err := captureSQLiteQuery(ctx, transaction, query)
		if err != nil {
			return SQLiteSnapshot{}, err
		}
		snapshot.Queries = append(snapshot.Queries, querySnapshot)
	}
	if err := transaction.Commit(); err != nil {
		return SQLiteSnapshot{}, fmt.Errorf("finish SQLite snapshot transaction: %w", err)
	}
	return snapshot, nil
}

func captureSQLiteQuery(ctx context.Context, transaction *sql.Tx, query SQLiteQuery) (SQLiteQuerySnapshot, error) {
	rows, err := transaction.QueryContext(ctx, query.SQL, query.Args...)
	if err != nil {
		return SQLiteQuerySnapshot{}, fmt.Errorf("run SQLite snapshot query %q: %w", query.Name, err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return SQLiteQuerySnapshot{}, fmt.Errorf("read SQLite snapshot columns for %q: %w", query.Name, err)
	}
	result := SQLiteQuerySnapshot{Name: query.Name, Columns: columns, Rows: make([][]SQLiteCell, 0)}
	for rows.Next() {
		values := make([]any, len(columns))
		destinations := make([]any, len(columns))
		for i := range values {
			destinations[i] = &values[i]
		}
		if err := rows.Scan(destinations...); err != nil {
			return SQLiteQuerySnapshot{}, fmt.Errorf("read SQLite snapshot row for %q: %w", query.Name, err)
		}
		row := make([]SQLiteCell, len(values))
		for i, value := range values {
			cell, err := sqliteCell(value)
			if err != nil {
				return SQLiteQuerySnapshot{}, fmt.Errorf("encode SQLite snapshot value for %q column %q: %w", query.Name, columns[i], err)
			}
			row[i] = cell
		}
		result.Rows = append(result.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return SQLiteQuerySnapshot{}, fmt.Errorf("iterate SQLite snapshot rows for %q: %w", query.Name, err)
	}
	return result, nil
}

func sqliteCell(value any) (SQLiteCell, error) {
	switch value := value.(type) {
	case nil:
		return SQLiteCell{Type: "null"}, nil
	case int64:
		return SQLiteCell{Type: "integer", Value: strconv.FormatInt(value, 10)}, nil
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return SQLiteCell{}, errors.New("non-finite SQLite real value")
		}
		return SQLiteCell{Type: "real", Value: strconv.FormatFloat(value, 'g', -1, 64)}, nil
	case bool:
		if value {
			return SQLiteCell{Type: "boolean", Value: "true"}, nil
		}
		return SQLiteCell{Type: "boolean", Value: "false"}, nil
	case string:
		return SQLiteCell{Type: "text", Value: value}, nil
	case []byte:
		return SQLiteCell{Type: "blob_base64", Value: base64.StdEncoding.EncodeToString(value)}, nil
	case time.Time:
		return SQLiteCell{Type: "time", Value: value.UTC().Format(time.RFC3339Nano)}, nil
	case json.Number:
		return SQLiteCell{Type: "number", Value: value.String()}, nil
	default:
		return SQLiteCell{}, fmt.Errorf("unsupported driver value type %T", value)
	}
}

// Save writes a stable, owner-only JSON snapshot beneath root.
func (snapshot SQLiteSnapshot) Save(root *Root, name string) (string, error) {
	if root == nil {
		return "", errors.New("SQLite snapshot requires a temporary root")
	}
	encoded, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode SQLite snapshot: %w", err)
	}
	encoded = append(encoded, '\n')
	path, err := root.writeFile(name, encoded)
	if err != nil {
		return "", fmt.Errorf("save SQLite snapshot %q: %w", name, err)
	}
	return path, nil
}
