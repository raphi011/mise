# Migrations

Hand-written goose migrations, embedded in the binary and applied by
`mise-server serve`/`migrate`. `sqlite/` and `postgres/` must describe the same
schema; `TestSchemaParity` compares them. Keep file numbers in step.

## Rebuilding a SQLite table

SQLite's `ALTER TABLE` can't change constraints, column types or foreign keys,
so such changes copy the table. Postgres does the same change with
`ALTER TABLE` in a normal migration.

goose runs every migration in a transaction, and `PRAGMA foreign_keys=OFF` is a
no-op inside a transaction. With foreign keys on, `DROP TABLE` of the old table
deletes every referencing row via `ON DELETE CASCADE` (or fails on `RESTRICT`).
Rebuilds therefore opt out of goose's transaction and manage their own:

```sql
-- +goose NO TRANSACTION
-- +goose Up
PRAGMA foreign_keys = OFF;
BEGIN;
CREATE TABLE things_new (...) STRICT;
INSERT INTO things_new (...) SELECT ... FROM things;
DROP TABLE things;
ALTER TABLE things_new RENAME TO things;
-- recreate the table's indexes and triggers here
-- Abort if the copy broke a reference: pragma foreign_key_check only
-- reports rows, so turn a non-zero count into a constraint error.
CREATE TEMP TABLE _fk_check (n INTEGER NOT NULL CHECK (n = 0));
INSERT INTO _fk_check SELECT count(*) FROM pragma_foreign_key_check;
DROP TABLE _fk_check;
COMMIT;
PRAGMA foreign_keys = ON;
```

This relies on every statement running on the same connection, which holds
because migrations run on the writer pool (`SetMaxOpenConns(1)`, no idle or
lifetime limit). If a statement fails, the migration stops with the
transaction open and foreign keys off; `serve`/`migrate` then exit, which
closes the connection and rolls back.

goose records a `NO TRANSACTION` migration's version only after the script's
own `COMMIT`. A crash between the two leaves the rebuild applied but
unrecorded, and the next start fails on the migration's first statement.
Recover by checking the schema matches the migration and inserting its row
into `goose_db_version` by hand. Keep such migrations to the rebuild alone so
that check stays simple.
