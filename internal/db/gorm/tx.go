// Package gorm provides GORM-based database operations for claude-mnemonic.
package gorm

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// immediateTx runs fn in a transaction that takes the database write lock up
// front ("BEGIN IMMEDIATE") and commits if fn returns nil.
//
// Use it for operations that read and then write. In WAL mode a deferred
// transaction that has read, and then finds another connection has committed
// since, cannot upgrade to a write: SQLite fails it at once with
// SQLITE_BUSY_SNAPSHOT and the busy timeout never applies. The worker writes
// concurrently (asynchronous vector sync), so that would surface as sporadic
// "database is locked" errors. Taking the lock first makes the operation wait
// its turn, within the busy timeout, instead.
//
// fn receives a handle bound to a single connection with GORM's own per-call
// transactions disabled (they would try to nest inside this one).
func immediateTx(ctx context.Context, db *gorm.DB, fn func(tx *gorm.DB) error) error {
	return db.WithContext(ctx).Connection(func(conn *gorm.DB) (err error) {
		conn = conn.Session(&gorm.Session{SkipDefaultTransaction: true})
		if err := conn.Exec("BEGIN IMMEDIATE").Error; err != nil {
			return fmt.Errorf("begin write transaction: %w", err)
		}
		committed := false
		defer func() {
			if !committed { // fn failed or panicked
				_ = conn.Exec("ROLLBACK").Error
			}
		}()
		if err := fn(conn); err != nil {
			return err
		}
		if err := conn.Exec("COMMIT").Error; err != nil {
			return fmt.Errorf("commit: %w", err)
		}
		committed = true
		return nil
	})
}
