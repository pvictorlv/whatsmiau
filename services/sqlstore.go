package services

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"
	"github.com/verbeux-ai/whatsmiau/env"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.uber.org/zap"
)

const (
	dialectPostgres = "postgres"

	dbPingTimeout     = 10 * time.Second
	dbUpgradeTimeout  = 60 * time.Second
	dbConnIdleTime    = 5 * time.Minute
	dbRetryFirstDelay = time.Second
	dbRetryMaxDelay   = 10 * time.Second
)

var sqlStoreInstance *sqlstore.Container

func SQLStore() *sqlstore.Container {
	if sqlStoreInstance == nil {
		container, err := openSQLStore(context.Background())
		if err != nil {
			zap.L().Panic("failed to start sqlstore", zap.Error(err))
		}

		sqlStoreInstance = container
	}

	return sqlStoreInstance
}

func openSQLStore(ctx context.Context) (*sqlstore.Container, error) {
	db, err := sql.Open(env.Env.DBDialect, env.Env.DBURL)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}
	limitPool(db, env.Env.DBDialect, env.Env.DBMaxOpenConns, env.Env.DBMaxIdleConns)

	if err := waitForDatabase(ctx, db.PingContext, env.Env.DBStartupTimeout, dbRetryFirstDelay); err != nil {
		_ = db.Close()
		return nil, err
	}

	container := sqlstore.NewWithDB(db, env.Env.DBDialect, nil)
	upgradeCtx, cancel := context.WithTimeout(ctx, dbUpgradeTimeout)
	defer cancel()
	if err := container.Upgrade(upgradeCtx); err != nil {
		_ = container.Close()
		return nil, fmt.Errorf("failed to upgrade database: %w", err)
	}

	return container, nil
}

// limitPool caps the Postgres connections. database/sql has no cap by default:
// a contact sync or an offline backlog opened one connection per concurrent
// query and took every slot of the server the CRM shares (#1637). Past the cap a
// query waits for a connection instead of being refused and losing the message.
func limitPool(db *sql.DB, dialect string, maxOpen, maxIdle int) {
	if dialect != dialectPostgres || maxOpen <= 0 {
		return
	}

	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(min(maxIdle, maxOpen))
	db.SetConnMaxIdleTime(dbConnIdleTime)
}

// waitForDatabase retries until the database accepts a connection. Dying on the
// first refusal made the process manager give up after a burst of restarts.
func waitForDatabase(ctx context.Context, ping func(context.Context) error, limit, firstDelay time.Duration) error {
	deadline := time.Now().Add(limit)
	delay := firstDelay

	for {
		pingCtx, cancel := context.WithTimeout(ctx, dbPingTimeout)
		err := ping(pingCtx)
		cancel()
		if err == nil {
			return nil
		}
		if time.Now().Add(delay).After(deadline) {
			return fmt.Errorf("database unavailable for %s: %w", limit, err)
		}

		zap.L().Warn("database unavailable, retrying", zap.Error(err), zap.Duration("delay", delay))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		delay = min(delay*2, dbRetryMaxDelay)
	}
}
