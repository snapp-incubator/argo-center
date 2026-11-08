// Package store opens the SQLite database that holds the audit log and rollouts.
package store

import (
	"database/sql"
	"fmt"

	// Pure Go (WebAssembly build of SQLite, no cgo).
	_ "github.com/ncruces/go-sqlite3/driver"
)

// Open opens (creating if needed) the database at path.
func Open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(wal)")
	if err != nil {
		return nil, err
	}
	// One connection serializes writers, which SQLite needs anyway.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	return db, nil
}
