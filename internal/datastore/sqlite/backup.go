package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	sqlite3 "github.com/mattn/go-sqlite3"
)

// Backup performs an online hot-backup of the database to destPath using the sqlite3_backup API.
// This is safe to call while other goroutines are reading/writing.
func (s *Store) Backup(ctx context.Context, destPath string) error {
	// Open a new driver connection for the destination.
	destDB, err := sql.Open("sqlite3", destPath)
	if err != nil {
		return fmt.Errorf("backup: open dest db: %w", err)
	}
	defer destDB.Close()

	// Get a raw connection from the source pool.
	srcConn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("backup: get src conn: %w", err)
	}
	defer srcConn.Close()

	// Get a raw connection from the destination pool.
	destConn, err := destDB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("backup: get dest conn: %w", err)
	}
	defer destConn.Close()

	return srcConn.Raw(func(srcRaw any) error {
		return destConn.Raw(func(destRaw any) error {
			srcSQLite, ok := srcRaw.(*sqlite3.SQLiteConn)
			if !ok {
				return fmt.Errorf("backup: src not a sqlite3.SQLiteConn")
			}
			destSQLite, ok := destRaw.(*sqlite3.SQLiteConn)
			if !ok {
				return fmt.Errorf("backup: dest not a sqlite3.SQLiteConn")
			}

			bk, err := destSQLite.Backup("main", srcSQLite, "main")
			if err != nil {
				return fmt.Errorf("backup: init sqlite3_backup: %w", err)
			}
			defer func() { _ = bk.Finish() }()

			for {
				select {
				case <-ctx.Done():
					return ctx.Err()
				default:
				}
				done, err := bk.Step(-1)
				if err != nil {
					return fmt.Errorf("backup: step: %w", err)
				}
				if done {
					return nil
				}
			}
		})
	})
}
