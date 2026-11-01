package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/meshcore-analyzer/dbschema"
)

func TestObserverIndexCLIRequiresExplicitOfflineTelemetryAction(t *testing.T) {
	t.Setenv("CORESCOPE_DATABASE_URL", "postgres://unused/target")
	t.Setenv("CORESCOPE_USERS_DATABASE_URL", "")
	for _, extra := range [][]string{nil, {"-check-ready"}, {"-storage-action=status"}, {"-from-sqlite=x"}, {"-users-database-url=postgres://unused/accounts"}, {"-backend=sqlite"}, {"-resume"}} {
		args := []string{"-ensure-observer-transmission-index"}
		if extra != nil {
			args = append(args, "-offline")
			args = append(args, extra...)
		}
		var output bytes.Buffer
		if err := runCommand(context.Background(), args, &output); err == nil || !strings.Contains(err.Error(), "-offline") {
			t.Fatalf("unsafe invocation accepted or opened a target: %v %v", args, err)
		}
	}
	t.Setenv("CORESCOPE_USERS_DATABASE_URL", "postgres://unused/accounts")
	if err := runCommand(context.Background(), []string{"-ensure-observer-transmission-index", "-offline"}, &bytes.Buffer{}); err == nil {
		t.Fatal("account environment was ignored")
	}
}

func TestObserverIndexCLIUpgradesExistingReadyTarget(t *testing.T) {
	dsn := postgresSchema(t)
	db := openImportDB(t, dsn)
	if err := dbschema.ApplyPostgres(db, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP INDEX idx_observations_observer_tx`); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CORESCOPE_DATABASE_URL", dsn)
	t.Setenv("CORESCOPE_USERS_DATABASE_URL", "")
	t.Setenv("CORESCOPE_USERS_OWNER_DATABASE_URL", "must-not-be-opened")
	for _, created := range []bool{true, false} {
		var output bytes.Buffer
		if err := runCommand(context.Background(), []string{"-ensure-observer-transmission-index", "-offline"}, &output); err != nil {
			t.Fatal(err)
		}
		want := `"created":false`
		if created {
			want = `"created":true`
		}
		if !strings.Contains(output.String(), want) || strings.Contains(output.String(), dsn) {
			t.Fatal("unexpected or private CLI output", output.String())
		}
	}
}

func TestObserverIndexCLINeverBootstrapsWrongTarget(t *testing.T) {
	dsn := postgresSchema(t)
	t.Setenv("CORESCOPE_DATABASE_URL", dsn)
	t.Setenv("CORESCOPE_USERS_DATABASE_URL", "")
	if err := runCommand(context.Background(), []string{"-ensure-observer-transmission-index", "-offline"}, &bytes.Buffer{}); err == nil {
		t.Fatal("empty target was accepted as installed telemetry")
	}
	var tables int
	if err := openImportDB(t, dsn).QueryRow(`SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema()`).Scan(&tables); err != nil || tables != 0 {
		t.Fatal("wrong target was bootstrapped", tables, err)
	}
}
