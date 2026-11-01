package dbschema

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// This physical performance index is optional for v1 readiness, allowing old
// installations and rollback binaries to remain readable during owner upgrades.
const ObserverTransmissionIndex = "idx_observations_observer_tx"

const observerTransmissionIndexDDL = `CREATE INDEX idx_observations_observer_tx ON observations(observer_idx, transmission_id)`

type ObserverIndexResult struct {
	Index   string `json:"index"`
	Created bool   `json:"created"`
}

type indexMetadataQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func postgresObserverIndex(ctx context.Context, db indexMetadataQuerier) (bool, error) {
	var correct bool
	err := db.QueryRowContext(ctx, `SELECT CASE WHEN c.relkind='i' THEN
		i.indisvalid AND i.indisready AND i.indislive AND NOT i.indisunique
		AND i.indrelid='observations'::regclass
		AND pg_get_indexdef(c.oid)=format('CREATE INDEX %I ON %I.observations USING btree (observer_idx, transmission_id)', $1::text, current_schema())
		ELSE false END
		FROM pg_class c LEFT JOIN pg_index i ON i.indexrelid=c.oid
		WHERE c.relnamespace=current_schema()::regnamespace AND c.relname=$1::text`, ObserverTransmissionIndex).Scan(&correct)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !correct {
		return false, errors.New("observer transmission index exists with an invalid state or unexpected definition")
	}
	return true, nil
}

// HasPostgresObserverTransmissionIndex is read-only and rejects a same-name
// object whose definition or validity differs from the approved index.
func HasPostgresObserverTransmissionIndex(ctx context.Context, db *sql.DB) (bool, error) {
	return postgresObserverIndex(ctx, db)
}

// EnsurePostgresObserverTransmissionIndex is an explicit offline owner action.
// It never changes readiness, logical schema versions, row data, or privileges.
func EnsurePostgresObserverTransmissionIndex(ctx context.Context, db *sql.DB) (*ObserverIndexResult, error) {
	if err := AssertReadyContext(ctx, db); err != nil {
		return nil, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var owns bool
	if err := tx.QueryRowContext(ctx, `SELECT pg_has_role(current_user,c.relowner,'USAGE') FROM pg_class c WHERE c.oid='observations'::regclass`).Scan(&owns); err != nil {
		return nil, err
	}
	if !owns {
		return nil, errors.New("observer index upgrade requires the telemetry table owner")
	}
	if _, err := tx.ExecContext(ctx, `SET LOCAL lock_timeout='5s'`); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(current_schema(),$1))`, WriterLockKey); err != nil {
		return nil, err
	}
	present, err := postgresObserverIndex(ctx, tx)
	if err != nil {
		return nil, err
	}
	if !present {
		if _, err := tx.ExecContext(ctx, observerTransmissionIndexDDL); err != nil {
			return nil, fmt.Errorf("build observer transmission index: %w", err)
		}
		if built, err := postgresObserverIndex(ctx, tx); err != nil {
			return nil, err
		} else if !built {
			return nil, errors.New("observer transmission index was not created")
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &ObserverIndexResult{Index: ObserverTransmissionIndex, Created: !present}, nil
}

func sqliteObserverIndex(ctx context.Context, db indexMetadataQuerier) (bool, error) {
	var kind, table string
	err := db.QueryRowContext(ctx, `SELECT type,tbl_name FROM sqlite_master WHERE name=?`, ObserverTransmissionIndex).Scan(&kind, &table)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if kind != "index" || table != "observations" {
		return false, errors.New("observer transmission index name is occupied by another object")
	}
	rows, err := db.QueryContext(ctx, `PRAGMA index_list(observations)`)
	if err != nil {
		return false, err
	}
	valid := false
	for rows.Next() {
		var seq, unique, partial int
		var name, origin string
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			rows.Close()
			return false, err
		}
		if name == ObserverTransmissionIndex {
			valid = unique == 0 && partial == 0
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	rows, err = db.QueryContext(ctx, `PRAGMA index_xinfo(idx_observations_observer_tx)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	want, position := []string{"observer_idx", "transmission_id"}, 0
	for rows.Next() {
		var seq, cid, desc, key int
		var name, collation sql.NullString
		if err := rows.Scan(&seq, &cid, &name, &desc, &collation, &key); err != nil {
			return false, err
		}
		if key == 0 {
			continue
		}
		if position >= len(want) || name.String != want[position] || cid < 0 || desc != 0 || collation.String != "BINARY" {
			valid = false
		}
		position++
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	if !valid || position != 2 {
		return false, errors.New("observer transmission index has an unexpected definition")
	}
	return true, nil
}

func HasSQLiteObserverTransmissionIndex(ctx context.Context, db *sql.DB) (bool, error) {
	return sqliteObserverIndex(ctx, db)
}

// SQLite creation remains owned by its writer or offline converter.
func EnsureSQLiteObserverTransmissionIndex(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	present, err := sqliteObserverIndex(ctx, tx)
	if err != nil {
		return err
	}
	if !present {
		if _, err := tx.ExecContext(ctx, observerTransmissionIndexDDL); err != nil {
			return err
		}
		if built, err := sqliteObserverIndex(ctx, tx); err != nil {
			return err
		} else if !built {
			return errors.New("observer transmission index was not created")
		}
	}
	return tx.Commit()
}
