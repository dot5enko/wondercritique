package dbx

import (
	"context"

	"gorm.io/gorm"
)

type txKey struct{}

// Tx runs fn inside a transaction carried by the ctx passed to fn; nested calls use savepoints.
// The transaction commits if fn returns nil and rolls back otherwise.
func Tx(ctx context.Context, db *gorm.DB, fn func(ctx context.Context) error) error {
	return Conn(ctx, db).Transaction(func(tx *gorm.DB) error {
		return fn(context.WithValue(ctx, txKey{}, tx))
	})
}

// Conn returns the transaction stored in ctx, or db bound to ctx when there is none.
func Conn(ctx context.Context, db *gorm.DB) *gorm.DB {
	if tx, ok := ctx.Value(txKey{}).(*gorm.DB); ok {
		return tx.WithContext(ctx)
	}
	return db.WithContext(ctx)
}

// InTx reports whether ctx carries a transaction.
func InTx(ctx context.Context) bool {
	_, ok := ctx.Value(txKey{}).(*gorm.DB)
	return ok
}
