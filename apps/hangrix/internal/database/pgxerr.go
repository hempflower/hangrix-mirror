package database

import (
	"errors"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"
)

// IsUniqueViolation reports whether err is (or wraps) a Postgres unique
// constraint violation (SQLSTATE 23505). Modules use this to translate
// the pgx error into a domain-level conflict sentinel without each
// repeating the same errors.As + code compare boilerplate.
func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UniqueViolation
}
