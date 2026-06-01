// Package tx defines the minimal interfaces for database transaction management.
// These are platform-level contracts implemented by concrete storage adapters
// (e.g. internal/platform/postgres) and consumed by domain services that own
// transaction boundaries (e.g. ledger.Service).
//
// No DB driver is imported here — the interfaces are pure Go.
package tx

import "context"

// Starter abstracts the ability to begin a database transaction.
// The concrete implementation lives in internal/platform/postgres.
type Starter interface {
	BeginTx(ctx context.Context) (Tx, error)
}

// Tx represents an in-progress database transaction.
type Tx interface {
	Commit() error
	Rollback() error
}
