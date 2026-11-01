package dbschema

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/meshcore-analyzer/pgutil"
)

func TestAssertReadyContextHonorsDeadline(t *testing.T) {
	db, err := pgutil.Open(postgresSchema(t), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Apply(db, nil); err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	held, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	// Release even for the old context-ignoring reader so RED terminates at
	// an assertion, rather than hanging on the occupied fixture connection.
	release := time.AfterFunc(100*time.Millisecond, func() { held.Close() })
	defer release.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := AssertReadyContext(ctx, db); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("readiness ignored request deadline: %v", err)
	}
}
