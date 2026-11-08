package store_test

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
)

// TestSchemaParity migrates both engines and compares tables, columns,
// nullability, type families, primary keys, unique indexes and foreign keys.
// The two migration directories are written by hand; this keeps them in step.
func TestSchemaParity(t *testing.T) {
	lite := describe(t, storetest.SQLite(t))
	pg := describe(t, storetest.Postgres(t))

	if diff := diffLines(lite, pg); diff != "" {
		t.Errorf("schemas differ (- sqlite, + postgres):\n%s", diff)
	}
}

// typeFamily maps engine types to a comparable family.
func typeFamily(d store.Dialect, typ string) string {
	typ = strings.ToLower(typ)
	if d == store.SQLite {
		// STRICT tables only allow these; TEXT covers ids, times and dates.
		switch typ {
		case "text":
			return "text"
		case "integer", "int":
			return "integer"
		case "real":
			return "real"
		case "blob":
			return "blob"
		}
		return "?" + typ
	}
	switch typ {
	case "text", "uuid", "timestamp with time zone", "date", "character varying", "jsonb":
		return "text"
	case "integer", "bigint", "smallint", "boolean":
		return "integer"
	case "numeric", "double precision", "real":
		return "real"
	case "bytea":
		return "blob"
	}
	return "?" + typ
}

// describe returns a sorted, line-per-fact description of the schema.
func describe(t *testing.T, db *store.DB) []string {
	t.Helper()
	ctx := context.Background()
	q := db.System().Read()
	var lines []string
	add := func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }

	var tables []string
	var err error
	switch db.Dialect() {
	case store.SQLite:
		err = q.Select(ctx, &tables, `SELECT name FROM sqlite_schema
			WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name <> 'goose_db_version' ORDER BY name`)
	case store.Postgres:
		err = q.Select(ctx, &tables, `SELECT table_name FROM information_schema.tables
			WHERE table_schema = current_schema() AND table_type = 'BASE TABLE' AND table_name <> 'goose_db_version'
			ORDER BY table_name`)
	}
	if err != nil {
		t.Fatal(err)
	}

	for _, tbl := range tables {
		add("table %s", tbl)
		switch db.Dialect() {
		case store.SQLite:
			describeSQLite(t, ctx, q, tbl, add)
		case store.Postgres:
			describePostgres(t, ctx, q, tbl, add)
		}
	}
	sort.Strings(lines)
	return lines
}

func describeSQLite(t *testing.T, ctx context.Context, q store.Reader, tbl string, add func(string, ...any)) {
	t.Helper()
	var cols []struct {
		Cid     int
		Name    string
		Type    string
		Notnull bool
		Dflt    *string `db:"dflt_value"`
		Pk      int
	}
	if err := q.Select(ctx, &cols, `SELECT * FROM pragma_table_info(?)`, tbl); err != nil {
		t.Fatal(err)
	}
	var pk []string
	slices.SortFunc(cols, func(a, b struct {
		Cid     int
		Name    string
		Type    string
		Notnull bool
		Dflt    *string `db:"dflt_value"`
		Pk      int
	},
	) int {
		return a.Pk - b.Pk
	})
	for _, c := range cols {
		// SQLite allows NULL in non-integer PRIMARY KEY columns unless NOT
		// NULL is declared; Postgres implies it. Treat PK columns as NOT NULL.
		add("column %s.%s %s notnull=%v", tbl, c.Name, typeFamily(store.SQLite, c.Type), c.Notnull || c.Pk > 0)
		if c.Pk > 0 {
			pk = append(pk, c.Name)
		}
	}
	add("pk %s(%s)", tbl, strings.Join(pk, ","))

	var idx []struct {
		Seq     int
		Name    string
		Unique  bool
		Origin  string
		Partial bool
	}
	if err := q.Select(ctx, &idx, `SELECT * FROM pragma_index_list(?)`, tbl); err != nil {
		t.Fatal(err)
	}
	for _, ix := range idx {
		if !ix.Unique || ix.Origin == "pk" {
			continue
		}
		var cols []string
		if err := q.Select(ctx, &cols, `SELECT name FROM pragma_index_info(?) ORDER BY seqno`, ix.Name); err != nil {
			t.Fatal(err)
		}
		add("unique %s(%s) partial=%v", tbl, strings.Join(cols, ","), ix.Partial)
	}

	var fks []struct {
		ID       int
		Seq      int
		Table    string
		From     string
		To       string
		OnUpdate string `db:"on_update"`
		OnDelete string `db:"on_delete"`
		Match    string
	}
	if err := q.Select(ctx, &fks, `SELECT * FROM pragma_foreign_key_list(?) ORDER BY id, seq`, tbl); err != nil {
		t.Fatal(err)
	}
	type fk struct {
		from, to []string
		ref, del string
	}
	byID := map[int]*fk{}
	var order []int
	for _, f := range fks {
		if byID[f.ID] == nil {
			byID[f.ID] = &fk{ref: f.Table, del: strings.ToLower(f.OnDelete)}
			order = append(order, f.ID)
		}
		byID[f.ID].from = append(byID[f.ID].from, f.From)
		byID[f.ID].to = append(byID[f.ID].to, f.To)
	}
	for _, id := range order {
		f := byID[id]
		add("fk %s(%s) -> %s(%s) on delete %s", tbl, strings.Join(f.from, ","), f.ref, strings.Join(f.to, ","), f.del)
	}
}

func describePostgres(t *testing.T, ctx context.Context, q store.Reader, tbl string, add func(string, ...any)) {
	t.Helper()
	var cols []struct {
		Name     string
		Type     string
		Nullable string
	}
	if err := q.Select(ctx, &cols, `SELECT column_name AS name, data_type AS type, is_nullable AS nullable
		FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = ?`, tbl); err != nil {
		t.Fatal(err)
	}
	for _, c := range cols {
		add("column %s.%s %s notnull=%v", tbl, c.Name, typeFamily(store.Postgres, c.Type), c.Nullable == "NO")
	}

	var idx []struct {
		Cols    string
		Primary bool
		Partial bool
	}
	if err := q.Select(ctx, &idx, `
		SELECT string_agg(a.attname, ',' ORDER BY k.ord) AS cols,
		       i.indisprimary AS primary,
		       i.indpred IS NOT NULL AS partial
		FROM pg_index i
		JOIN pg_class c ON c.oid = i.indrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		CROSS JOIN LATERAL unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord)
		JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = k.attnum
		WHERE n.nspname = current_schema() AND c.relname = ? AND i.indisunique
		GROUP BY i.indexrelid, i.indisprimary, i.indpred`, tbl); err != nil {
		t.Fatal(err)
	}
	for _, ix := range idx {
		if ix.Primary {
			add("pk %s(%s)", tbl, ix.Cols)
			continue
		}
		add("unique %s(%s) partial=%v", tbl, ix.Cols, ix.Partial)
	}

	var fks []struct {
		From string
		Ref  string
		To   string
		Del  string
	}
	if err := q.Select(ctx, &fks, `
		SELECT (SELECT string_agg(a.attname, ',' ORDER BY k.ord)
		          FROM unnest(con.conkey) WITH ORDINALITY AS k(attnum, ord)
		          JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.attnum) AS "from",
		       ref.relname AS ref,
		       (SELECT string_agg(a.attname, ',' ORDER BY k.ord)
		          FROM unnest(con.confkey) WITH ORDINALITY AS k(attnum, ord)
		          JOIN pg_attribute a ON a.attrelid = con.confrelid AND a.attnum = k.attnum) AS "to",
		       CASE con.confdeltype WHEN 'a' THEN 'no action' WHEN 'r' THEN 'restrict' WHEN 'c' THEN 'cascade'
		            WHEN 'n' THEN 'set null' WHEN 'd' THEN 'set default' END AS del
		FROM pg_constraint con
		JOIN pg_class c ON c.oid = con.conrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_class ref ON ref.oid = con.confrelid
		WHERE con.contype = 'f' AND n.nspname = current_schema() AND c.relname = ?`, tbl); err != nil {
		t.Fatal(err)
	}
	for _, f := range fks {
		add("fk %s(%s) -> %s(%s) on delete %s", tbl, f.From, f.Ref, f.To, f.Del)
	}
}

func diffLines(a, b []string) string {
	var out strings.Builder
	inB := map[string]bool{}
	for _, l := range b {
		inB[l] = true
	}
	inA := map[string]bool{}
	for _, l := range a {
		inA[l] = true
		if !inB[l] {
			out.WriteString("- " + l + "\n")
		}
	}
	for _, l := range b {
		if !inA[l] {
			out.WriteString("+ " + l + "\n")
		}
	}
	return out.String()
}
