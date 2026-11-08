package store

import (
	"strconv"
	"strings"
	"sync"
)

// Dialect identifies the database engine.
type Dialect int

const (
	SQLite Dialect = iota + 1
	Postgres
)

func (d Dialect) String() string {
	switch d {
	case SQLite:
		return "sqlite"
	case Postgres:
		return "postgres"
	default:
		return "unknown"
	}
}

var rebindCache sync.Map // string -> string

// rebind rewrites ? placeholders to $1, $2, ... for Postgres. Question marks
// inside string literals, quoted identifiers and comments are left alone.
//
// The scanner knows only standard SQL quoting. Postgres E'...' escape
// strings (a \' would end the literal early), $$ dollar quoting and the
// jsonb ?, ?| and ?& operators are not supported: their ? would be rewritten.
// Don't use them in queries.
func (d Dialect) rebind(query string) string {
	if d != Postgres || !strings.Contains(query, "?") {
		return query
	}
	if v, ok := rebindCache.Load(query); ok {
		return v.(string)
	}
	out := rebindPositional(query)
	rebindCache.Store(query, out)
	return out
}

func rebindPositional(q string) string {
	var b strings.Builder
	b.Grow(len(q) + 8)
	n := 0
	for i := 0; i < len(q); i++ {
		c := q[i]
		switch {
		case c == '\'' || c == '"':
			// Copy through the closing quote; a doubled quote is an escape.
			j := i + 1
			for j < len(q) {
				if q[j] == c {
					if j+1 < len(q) && q[j+1] == c {
						j += 2
						continue
					}
					break
				}
				j++
			}
			end := min(j+1, len(q))
			b.WriteString(q[i:end])
			i = end - 1
		case c == '-' && i+1 < len(q) && q[i+1] == '-':
			end := len(q)
			if k := strings.IndexByte(q[i:], '\n'); k >= 0 {
				end = i + k
			}
			b.WriteString(q[i:end])
			i = end - 1
		case c == '/' && i+1 < len(q) && q[i+1] == '*':
			end := len(q)
			if k := strings.Index(q[i+2:], "*/"); k >= 0 {
				end = i + 2 + k + 2
			}
			b.WriteString(q[i:end])
			i = end - 1
		case c == '?':
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
