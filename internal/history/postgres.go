package history

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/cpanato/tado-bridge/internal/domain"
	"github.com/cpanato/tado-bridge/internal/store"
)

//go:embed schema.sql
var schema string

type Postgres struct {
	db        *sql.DB
	retention time.Duration
	now       func() time.Time
}

// Open checks connectivity and creates the initial schema before serving. The
// caller owns Close; all runtime operations have independent five-second limits.
func Open(ctx context.Context, databaseURL string, retention time.Duration) (*Postgres, error) {
	if err := ValidateURL(databaseURL); err != nil {
		return nil, err
	}
	if retention < 24*time.Hour || retention > 3650*24*time.Hour {
		return nil, errors.New("history retention must be between 24h and 87600h")
	}
	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return nil, errors.New("invalid history PostgreSQL configuration")
	}
	config.ConnectTimeout = 5 * time.Second
	db := stdlib.OpenDB(*config)
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxIdleTime(5 * time.Minute)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := initialize(ctx, db); err != nil {
		_ = db.Close()
		return nil, errors.New("history PostgreSQL initialization failed; check connectivity and schema permissions")
	}
	return &Postgres{db: db, retention: retention, now: time.Now}, nil
}

func initialize(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// Serialize initial schema creation across simultaneous process starts.
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(746236941)"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, summarySchema); err != nil {
		return err
	}
	return tx.Commit()
}

func (p *Postgres) Close() error { return p.db.Close() }

// Save stores a validated full snapshot and removes expired rows atomically.
// Duplicate collection timestamps are idempotent. Failed writes are never
// retried through the provider and do not replace the current cache.
func (p *Postgres) Save(ctx context.Context, snapshot domain.Snapshot) error {
	var validated store.Store
	if err := validated.Save(snapshot); err != nil {
		return err
	}
	view, _ := validated.Load()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("history write failed")
	}
	defer func() { _ = tx.Rollback() }()
	// PostgreSQL stores microseconds; use that same precision for cursor keys.
	at := snapshot.CollectedAt.UTC().Truncate(time.Microsecond)
	if _, err := tx.ExecContext(ctx, `INSERT INTO tado_bridge_snapshots (collected_at, payload) VALUES ($1, $2::jsonb) ON CONFLICT (collected_at) DO NOTHING`, at, string(view.JSON)); err != nil {
		return errors.New("history write failed")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM tado_bridge_snapshots WHERE collected_at < $1`, p.now().Add(-p.retention)); err != nil {
		return errors.New("history retention cleanup failed")
	}
	if err := tx.Commit(); err != nil {
		return errors.New("history write failed")
	}
	return nil
}

func (p *Postgres) Query(ctx context.Context, query Query) (Page, error) {
	if err := query.Validate(); err != nil {
		return Page{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rows, err := p.db.QueryContext(ctx, `SELECT collected_at, payload FROM tado_bridge_snapshots WHERE collected_at >= $1 AND collected_at < $2 ORDER BY collected_at DESC LIMIT $3`, maxTime(query.From, p.now().Add(-p.retention)), query.To, query.Limit+1)
	if err != nil {
		return Page{}, errors.New("history query failed")
	}
	defer func() { _ = rows.Close() }()
	page := Page{Snapshots: []json.RawMessage{}}
	var last time.Time
	size := 0
	for rows.Next() {
		var at time.Time
		var data []byte
		if err := rows.Scan(&at, &data); err != nil {
			return Page{}, errors.New("history query failed")
		}
		if len(page.Snapshots) == query.Limit || (len(page.Snapshots) > 0 && size+len(data) > maxPageBytes) {
			cursor := last.UTC()
			page.NextTo = &cursor
			break
		}
		if len(data) > maxPageBytes || !json.Valid(data) {
			return Page{}, errors.New("invalid stored history snapshot")
		}
		page.Snapshots = append(page.Snapshots, json.RawMessage(data))
		size += len(data)
		last = at
	}
	if rows.Err() != nil {
		return Page{}, errors.New("history query failed")
	}
	return page, nil
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
