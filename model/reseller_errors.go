package model

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
)

// Preserve the common conflict identity for domain callers while giving the
// management API a stable reason for each recoverable conflict.
var (
	ErrResellerRevisionConflict  = fmt.Errorf("revision changed: %w", ErrResellerConflict)
	ErrResellerOperationConflict = fmt.Errorf("operation parameters changed: %w", ErrResellerConflict)
	ErrResellerBalanceConflict   = fmt.Errorf("balance changed or unavailable: %w", ErrResellerConflict)
	ErrResellerDuplicateResource = fmt.Errorf("duplicate resource: %w", ErrResellerConflict)
	ErrResellerDataCorrupt       = errors.New("invalid persisted reseller configuration")
)

// ResellerRevisionConflictError carries only the authorized subject's revision,
// never its configuration or another subject's state.
type ResellerRevisionConflictError struct {
	Revision int64
}

func (e *ResellerRevisionConflictError) Error() string { return ErrResellerRevisionConflict.Error() }
func (e *ResellerRevisionConflictError) Unwrap() error { return ErrResellerRevisionConflict }

// IsResellerDatabaseError recognizes database/connection failures without
// turning an arbitrary internal error into a retryable persistence outage.
func IsResellerDatabaseError(err error) bool {
	var mysqlError *mysqlDriver.MySQLError
	var postgresError *pgconn.PgError
	var postgresConnectionError *pgconn.ConnectError
	var sqliteError interface{ Code() int }
	var networkError net.Error
	return errors.Is(err, ErrDatabase) || errors.Is(err, driver.ErrBadConn) || errors.Is(err, sql.ErrConnDone) ||
		errors.As(err, &mysqlError) || errors.As(err, &postgresError) || errors.As(err, &postgresConnectionError) ||
		errors.As(err, &sqliteError) || errors.As(err, &networkError)
}
