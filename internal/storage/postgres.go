package storage

import (
	"context"
	"database/sql"
	"fmt"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/kanpon/data-governance/internal/storage/ent"
)

type postgresStorage struct {
	db     *sql.DB
	client *ent.Client
}

// NewPostgres opens a pgx-backed *sql.DB and wraps it in an ent client.
// dsn must use the "postgres://user:pw@host:port/db?sslmode=..." form.
func NewPostgres(ctx context.Context, dsn string) (Storage, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	return &postgresStorage{db: db, client: client}, nil
}

func (s *postgresStorage) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }
func (s *postgresStorage) Ent() *ent.Client               { return s.client }
func (s *postgresStorage) DB() *sql.DB                    { return s.db }

func (s *postgresStorage) WithTx(ctx context.Context, fn func(tx *ent.Tx) error) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if r := recover(); r != nil {
			_ = tx.Rollback()
			panic(r)
		}
	}()
	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			return fmt.Errorf("%w (rollback failed: %v)", err, rbErr)
		}
		return err
	}
	return tx.Commit()
}

func (s *postgresStorage) Close() error {
	if err := s.client.Close(); err != nil {
		return err
	}
	return s.db.Close()
}
