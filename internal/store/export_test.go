package store

import "context"

// MigrateDownTo rolls migrations back until version is the newest applied.
func (db *DB) MigrateDownTo(ctx context.Context, version int64) error {
	p, err := db.migrator()
	if err != nil {
		return err
	}
	_, err = p.DownTo(ctx, version)
	return err
}
