package services

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
)

var errTooManyClients = errors.New("pq: sorry, too many clients already")

func TestWaitForDatabaseRetriesUntilItAccepts(t *testing.T) {
	calls := 0
	ping := func(context.Context) error {
		calls++
		if calls < 4 {
			return errTooManyClients
		}
		return nil
	}

	if err := waitForDatabase(context.Background(), ping, time.Second, time.Millisecond); err != nil {
		t.Fatalf("expected the database to be reached, got %v", err)
	}
	if calls != 4 {
		t.Fatalf("expected 4 attempts, got %d", calls)
	}
}

func TestWaitForDatabaseGivesUpWithTheCause(t *testing.T) {
	ping := func(context.Context) error { return errTooManyClients }

	err := waitForDatabase(context.Background(), ping, 20*time.Millisecond, time.Millisecond)
	if !errors.Is(err, errTooManyClients) {
		t.Fatalf("expected the last refusal as the cause, got %v", err)
	}
}

func TestWaitForDatabaseStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ping := func(context.Context) error {
		cancel()
		return errTooManyClients
	}

	if err := waitForDatabase(ctx, ping, time.Hour, time.Minute); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestLimitPool(t *testing.T) {
	cases := []struct {
		name          string
		dialect       string
		maxOpen, want int
	}{
		{"postgres is capped", "postgres", 40, 40},
		{"zero keeps postgres uncapped", "postgres", 0, 0},
		{"sqlite is left alone", "sqlite3", 40, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, err := sql.Open("postgres", "postgres://u:p@127.0.0.1:1/none?sslmode=disable")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()

			limitPool(db, tc.dialect, tc.maxOpen, 10)
			if got := db.Stats().MaxOpenConnections; got != tc.want {
				t.Fatalf("MaxOpenConnections = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestPoolUnderContactBurst reproduces the #1637 burst against a real Postgres:
// far more concurrent store calls than connections, part of them inside the
// decryption transaction. It must neither exceed the cap nor deadlock.
// Run with TEST_DB_URL pointing at a scratch database named "pooltest".
func TestPoolUnderContactBurst(t *testing.T) {
	url := os.Getenv("TEST_DB_URL")
	if url == "" {
		t.Skip("TEST_DB_URL not set")
	}
	if !strings.Contains(url, "/pooltest") {
		t.Fatal("refusing to run outside the pooltest database")
	}

	const maxOpen, workers, rounds = 5, 400, 5
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	limitPool(db, "postgres", maxOpen, maxOpen)
	container := sqlstore.NewWithDB(db, "postgres", nil)
	defer container.Close()
	if err := container.Upgrade(ctx); err != nil {
		t.Fatal(err)
	}

	store := sqlstore.NewSQLStore(container, types.NewJID("5500000000000", types.DefaultUserServer))
	var failures, overCap atomic.Int64
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := range rounds {
				user := fmt.Sprintf("%d", 100000+w*rounds+r)
				lid := types.NewJID(user, types.HiddenUserServer)
				_, lookupErr := container.LIDMap.GetPNForLID(ctx, lid)
				txErr := store.DoDecryptionTxn(ctx, func(txCtx context.Context) error {
					_, err := store.GetSession(txCtx, user)
					return err
				})
				if lookupErr != nil || txErr != nil {
					failures.Add(1)
				}
				if db.Stats().OpenConnections > maxOpen {
					overCap.Add(1)
				}
			}
		}()
	}
	wg.Wait()

	if n := failures.Load(); n > 0 {
		t.Errorf("%d calls failed under the cap", n)
	}
	if n := overCap.Load(); n > 0 {
		t.Errorf("the pool exceeded the cap %d times", n)
	}
}
