package audit_test

import (
	"context"
	"os"
	"testing"

	"beeloy.com/backend/internal/audit"
	"beeloy.com/backend/internal/platform/db"
	"beeloy.com/backend/internal/platform/pagination"
	"beeloy.com/backend/internal/testutil"
	"errors"
	"github.com/jackc/pgx/v5"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testutil.Cleanup()
	os.Exit(code)
}

func TestRecordRollsBackWithTx(t *testing.T) {
	pool := testutil.PG(t)
	ctx := context.Background()
	svc := audit.Service{}
	boom := errors.New("boom")
	err := db.WithTx(ctx, pool, func(tx pgx.Tx) error {
		if err := svc.Record(ctx, tx, audit.Input{Action: audit.ActionLogin, Meta: map[string]any{"provider": "email_otp"}}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	evs, _, _ := svc.List(ctx, pool, audit.Filter{}, pagination.Cursor{}, 10)
	if len(evs) != 0 {
		t.Fatal("rolled back audit event must not exist")
	}
	for i := 0; i < 5; i++ {
		if err := svc.Record(ctx, pool, audit.Input{Action: audit.ActionLogin}); err != nil {
			t.Fatal(err)
		}
	}
	p1, next, err := svc.List(ctx, pool, audit.Filter{}, pagination.Cursor{}, 3)
	if err != nil || len(p1) != 3 || next == "" {
		t.Fatalf("page1: %d %q %v", len(p1), next, err)
	}
	cur, _ := pagination.Decode(next)
	p2, next2, _ := svc.List(ctx, pool, audit.Filter{}, cur, 3)
	if len(p2) != 2 || next2 != "" {
		t.Fatalf("page2: %d %q", len(p2), next2)
	}
}
