package postgres

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	maxOpenConnections = 10
	maxIdleConnections = 5
)

func Open(ctx context.Context, databaseURL string) (*sql.DB, error) {
	parsed, err := url.Parse(databaseURL)
	if err != nil ||
		(parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") ||
		parsed.Host == "" || parsed.Path == "" || parsed.Path == "/" {
		return nil, errors.New("DATABASE_URL must be a PostgreSQL URL")
	}

	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, errors.New("could not configure PostgreSQL connection")
	}
	db.SetMaxOpenConns(maxOpenConnections)
	db.SetMaxIdleConns(maxIdleConnections)
	db.SetConnMaxIdleTime(5 * time.Minute)
	db.SetConnMaxLifetime(30 * time.Minute)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, errors.New("could not connect to PostgreSQL")
	}
	return db, nil
}
