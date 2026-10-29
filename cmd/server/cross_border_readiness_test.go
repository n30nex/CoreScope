package main

import (
	"context"
	"errors"
	"testing"

	"github.com/meshcore-analyzer/dbconfig"
	"github.com/meshcore-analyzer/dbschema"
	"github.com/meshcore-analyzer/pgutil/pgtest"
)

func crossBorderReadyPostgresFixture(t *testing.T) (*DB, string) {
	t.Helper()
	dsn := pgtest.NewDatabase(t)
	conn, err := openFixtureSQL(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := dbschema.Apply(conn, nil); err != nil {
		t.Fatal(err)
	}
	db := &DB{conn: conn, path: dsn, backend: dbconfig.Postgres}
	if err := db.detectSchema(context.Background(), conn); err != nil {
		t.Fatal(err)
	}
	return db, dsn
}

func TestCrossBorderPeerRejectsUnreadyAndUnsupportedSchema(t *testing.T) {
	postgresOnly(t)
	local := crossBorderFixture(t)
	owner, dsn := crossBorderReadyPostgresFixture(t)
	readerURL := pgtest.ReadOnly(t, dsn)
	for _, tc := range []struct {
		name    string
		ready   bool
		version int
	}{
		{"import-incomplete", false, dbschema.Version},
		{"unsupported-version", true, dbschema.Version + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			crossBorderExec(t, owner, `UPDATE corescope_schema SET ready=$1,version=$2 WHERE kind='telemetry'`, tc.ready, tc.version)
			peer, err := openCrossBorderPeer(context.Background(), local, readerURL, crossBorderTestConfig())
			if err == nil {
				peer.conn.Close()
				t.Fatal("structurally complete peer bypassed shared readiness/version gate")
			}
			if !errors.Is(err, errCrossBorderUnavailable) {
				t.Fatalf("readiness details escaped sanitized peer error: %v", err)
			}
		})
	}
	crossBorderExec(t, owner, `UPDATE corescope_schema SET ready=true,version=$1 WHERE kind='telemetry'`, dbschema.Version)
	peer, err := openCrossBorderPeer(context.Background(), local, readerURL, crossBorderTestConfig())
	if err != nil {
		t.Fatal("recovered peer was not accepted", err)
	}
	peer.conn.Close()
}

func TestCrossBorderWarmPeerRechecksReadinessAndRecovers(t *testing.T) {
	postgresOnly(t)
	local := crossBorderFixture(t)
	owner, dsn := crossBorderReadyPostgresFixture(t)
	svc := &crossBorderService{cfg: crossBorderTestConfig(), local: local, peerURL: pgtest.ReadOnly(t, dsn)}
	t.Cleanup(func() { (&Server{crossBorder: svc}).closeCrossBorder() })
	if _, err := svc.loadReport(context.Background(), crossBorderTestFilters()); err != nil {
		t.Fatal("warm ready peer", err)
	}
	warm := svc.peer
	if warm == nil {
		t.Fatal("fixture did not warm peer pool")
	}
	for _, tc := range []struct {
		name    string
		ready   bool
		version int
	}{
		{"import-incomplete", false, dbschema.Version},
		{"unsupported-version", true, dbschema.Version + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			crossBorderExec(t, owner, `UPDATE corescope_schema SET ready=$1,version=$2 WHERE kind='telemetry'`, tc.ready, tc.version)
			if _, err := svc.loadReport(context.Background(), crossBorderTestFilters()); !errors.Is(err, errCrossBorderUnavailable) {
				t.Errorf("warm peer bypassed changed readiness state: %v", err)
			}
			crossBorderExec(t, owner, `UPDATE corescope_schema SET ready=true,version=$1 WHERE kind='telemetry'`, dbschema.Version)
			if _, err := svc.loadReport(context.Background(), crossBorderTestFilters()); err != nil {
				t.Errorf("peer readiness recovery remained stuck: %v", err)
			}
			if svc.peer != warm {
				t.Error("readiness transition unnecessarily replaced the bounded pool")
			}
		})
	}
}
