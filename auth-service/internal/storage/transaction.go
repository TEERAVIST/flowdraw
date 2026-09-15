package storage

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	fosstorage "github.com/ory/fosite/storage"
)

type transactionKey struct{}
type executor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (s *OAuthStore) executor(ctx context.Context) executor {
	if tx, ok := ctx.Value(transactionKey{}).(pgx.Tx); ok {
		return tx
	}
	return s.DB
}
func (s *OAuthStore) BeginTX(ctx context.Context) (context.Context, error) {
	if ctx.Value(transactionKey{}) != nil {
		return nil, errors.New("nested OAuth transaction")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return context.WithValue(ctx, transactionKey{}, tx), nil
}
func (s *OAuthStore) Commit(ctx context.Context) error {
	return ctx.Value(transactionKey{}).(pgx.Tx).Commit(ctx)
}
func (s *OAuthStore) Rollback(ctx context.Context) error {
	return ctx.Value(transactionKey{}).(pgx.Tx).Rollback(ctx)
}

var _ fosstorage.Transactional = (*OAuthStore)(nil)
