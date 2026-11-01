package dbschema

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/meshcore-analyzer/pgutil"
	"github.com/meshcore-analyzer/pgutil/pgtest"
)

func TestFreshPostgresObserverTransmissionIndex(t *testing.T) {
	db, err := pgutil.Open(postgresSchema(t), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := ApplyPostgres(db, nil); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM pg_indexes WHERE schemaname=current_schema() AND indexname='idx_observations_observer_tx'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("fresh schema lacks the observer/transmission covering index: count=%d err=%v", count, err)
	}
}

func observerIndexFixture(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dsn := postgresSchema(t)
	db, err := pgutil.Open(dsn, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := ApplyPostgres(db, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP INDEX idx_observations_observer_tx`); err != nil {
		t.Fatal(err)
	}
	return db, dsn
}

func TestObserverIndexExistingReadyAndIdempotent(t *testing.T) {
	db, _ := observerIndexFixture(t)
	if _, err := db.Exec(`INSERT INTO transmissions(id,hash,raw_hex,first_seen) VALUES(1,'preserved','AABB','2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		result, err := EnsurePostgresObserverTransmissionIndex(context.Background(), db)
		if err != nil || result.Created != (i == 0) {
			t.Fatalf("upgrade %d: %+v %v", i, result, err)
		}
	}
	if present, err := HasPostgresObserverTransmissionIndex(context.Background(), db); err != nil || !present {
		t.Fatal(present, err)
	}
	var raw string
	if err := db.QueryRow(`SELECT raw_hex FROM transmissions WHERE id=1`).Scan(&raw); err != nil || raw != "AABB" {
		t.Fatal("row changed", err)
	}
	if err := AssertReady(db); err != nil {
		t.Fatal(err)
	}
}

func TestObserverIndexRejectsUnexpectedDefinitions(t *testing.T) {
	for _, definition := range []string{
		`CREATE INDEX idx_observations_observer_tx ON observations(transmission_id,observer_idx)`,
		`CREATE INDEX idx_observations_observer_tx ON observations(observer_idx,transmission_id) WHERE observer_idx IS NOT NULL`,
		`CREATE UNIQUE INDEX idx_observations_observer_tx ON observations(observer_idx,transmission_id)`,
		`CREATE INDEX idx_observations_observer_tx ON observations(observer_idx DESC,transmission_id)`,
		`CREATE INDEX idx_observations_observer_tx ON observations(observer_idx) INCLUDE(transmission_id)`,
		`CREATE TABLE idx_observations_observer_tx(id INTEGER)`,
	} {
		t.Run(definition, func(t *testing.T) {
			db, _ := observerIndexFixture(t)
			if _, err := db.Exec(definition); err != nil {
				t.Fatal(err)
			}
			if _, err := EnsurePostgresObserverTransmissionIndex(context.Background(), db); err == nil {
				t.Fatal("unexpected object was accepted")
			}
		})
	}
}

func TestObserverIndexOwnerOnlyAndWrongTargets(t *testing.T) {
	db, dsn := observerIndexFixture(t)
	for _, role := range []string{pgtest.ReadOnly(t, dsn), pgtest.Writer(t, dsn)} {
		runtime, err := pgutil.Open(role, false)
		if err != nil {
			t.Fatal(err)
		}
		_, err = EnsurePostgresObserverTransmissionIndex(context.Background(), runtime)
		runtime.Close()
		if err == nil {
			t.Fatal("runtime role was allowed to upgrade indexes")
		}
	}
	if present, err := HasPostgresObserverTransmissionIndex(context.Background(), db); err != nil || present {
		t.Fatal("denied operation changed the schema", present, err)
	}
	for _, marker := range []string{`UPDATE corescope_schema SET ready=false`, `UPDATE corescope_schema SET ready=true,version=999`, `DELETE FROM corescope_schema WHERE kind='telemetry'`} {
		if _, err := db.Exec(marker); err != nil {
			t.Fatal(err)
		}
		if _, err := EnsurePostgresObserverTransmissionIndex(context.Background(), db); err == nil {
			t.Fatal("unsupported target accepted")
		}
	}
}

func TestObserverIndexCancellationIsAtomic(t *testing.T) {
	db, dsn := observerIndexFixture(t)
	blocker, err := pgutil.Open(dsn, false)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	lock, err := blocker.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback()
	if _, err := lock.Exec(`LOCK TABLE observations IN ROW EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err = EnsurePostgresObserverTransmissionIndex(ctx, db)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected cancelled build: %v", err)
	}
	if present, err := HasPostgresObserverTransmissionIndex(context.Background(), db); err != nil || present {
		t.Fatal("cancelled build left an index", present, err)
	}
	if err := lock.Rollback(); err != nil {
		t.Fatal(err)
	}
	if result, err := EnsurePostgresObserverTransmissionIndex(context.Background(), db); err != nil || !result.Created {
		t.Fatal(result, err)
	}
}

func TestObserverIndexRejectsInvalidConcurrentRemnant(t *testing.T) {
	db, _ := observerIndexFixture(t)
	if _, err := db.Exec(`INSERT INTO transmissions(id,hash,raw_hex,first_seen) VALUES(1,'held','AA','2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	snapshot, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Rollback()
	// A concurrent build installs its invalid catalog entry, then waits for
	// this older writer before scanning. Cancelling there leaves a real remnant.
	if _, err := snapshot.Exec(`INSERT INTO observations(transmission_id,timestamp) VALUES(1,1)`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	creator, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer creator.Close()
	var creatorPID int
	if err := creator.QueryRowContext(context.Background(), `SELECT pg_backend_pid()`).Scan(&creatorPID); err != nil {
		t.Fatal(err)
	}
	_, err = creator.ExecContext(ctx, `CREATE INDEX CONCURRENTLY idx_observations_observer_tx ON observations(observer_idx,transmission_id)`)
	if err == nil {
		t.Fatal("fixture concurrent build did not wait for its older snapshot")
	}
	// A cancelled client can return before its PostgreSQL backend observes the
	// disconnect. Cancel only this fixture's captured backend before releasing
	// the blocking writer, otherwise the build could race to valid afterwards.
	var requested bool
	if err := db.QueryRow(`SELECT pg_cancel_backend($1)`, creatorPID).Scan(&requested); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		var active bool
		if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid=$1 AND state='active')`, creatorPID).Scan(&active); err != nil {
			t.Fatal(err)
		}
		if !active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture index backend did not acknowledge cancellation")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := snapshot.Rollback(); err != nil {
		t.Fatal(err)
	}
	var valid bool
	if err := db.QueryRow(`SELECT i.indisvalid FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid WHERE c.relnamespace=current_schema()::regnamespace AND c.relname=$1`, ObserverTransmissionIndex).Scan(&valid); err != nil || valid {
		t.Fatalf("invalid-index fixture not established: %v %v", valid, err)
	}
	if _, err := EnsurePostgresObserverTransmissionIndex(context.Background(), db); err == nil {
		t.Fatal("invalid same-definition index accepted")
	}
}

func TestSQLiteObserverTransmissionIndex(t *testing.T) {
	for _, bad := range []string{"", "transmission_id,observer_idx", "observer_idx DESC,transmission_id"} {
		t.Run(bad, func(t *testing.T) {
			db, _ := sqliteTestDB(t)
			sqliteExec(t, db, `CREATE TABLE observations(observer_idx INTEGER,transmission_id INTEGER)`)
			if bad != "" {
				sqliteExec(t, db, fmt.Sprintf(`CREATE INDEX idx_observations_observer_tx ON observations(%s)`, bad))
				if err := EnsureSQLiteObserverTransmissionIndex(context.Background(), db); err == nil {
					t.Fatal("wrong SQLite index accepted")
				}
				return
			}
			for i := 0; i < 2; i++ {
				if err := EnsureSQLiteObserverTransmissionIndex(context.Background(), db); err != nil {
					t.Fatal(err)
				}
			}
			if present, err := HasSQLiteObserverTransmissionIndex(context.Background(), db); err != nil || !present {
				t.Fatal(present, err)
			}
		})
	}
}
