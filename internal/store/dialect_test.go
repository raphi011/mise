package store

import "testing"

func TestRebind(t *testing.T) {
	cases := []struct{ in, want string }{
		{"SELECT 1", "SELECT 1"},
		{"SELECT * FROM t WHERE a = ? AND b = ?", "SELECT * FROM t WHERE a = $1 AND b = $2"},
		{"SELECT '?' , ?", "SELECT '?' , $1"},
		{"SELECT 'it''s ?', ?", "SELECT 'it''s ?', $1"},
		{`SELECT "we?ird", ?`, `SELECT "we?ird", $1`},
		{"SELECT ? -- why?\n, ?", "SELECT $1 -- why?\n, $2"},
		{"SELECT /* ? */ ?", "SELECT /* ? */ $1"},
		{"SELECT 'unterminated ?", "SELECT 'unterminated ?"},
		{"SELECT ? /* unterminated ?", "SELECT $1 /* unterminated ?"},
	}
	for _, c := range cases {
		if got := Postgres.rebind(c.in); got != c.want {
			t.Errorf("rebind(%q) = %q, want %q", c.in, got, c.want)
		}
		if got := SQLite.rebind(c.in); got != c.in {
			t.Errorf("sqlite rebind changed %q to %q", c.in, got)
		}
	}
}
