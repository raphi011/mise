package store

import (
	"database/sql/driver"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// NewID returns a new time-ordered UUIDv7.
func NewID() uuid.UUID {
	return uuid.Must(uuid.NewV7())
}

// timeLayout is fixed width so that SQLite TEXT columns compare correctly as
// strings. Postgres parses it as timestamptz.
const timeLayout = "2006-01-02T15:04:05.000000Z"

// Time is a UTC timestamp with microsecond precision that stores identically
// on SQLite (fixed-width TEXT) and Postgres (timestamptz). Always bind times
// through this type; never use CURRENT_TIMESTAMP or now() in SQL.
//
// For nullable columns use *Time: a nil pointer binds as NULL and scans from
// NULL.
type Time struct{ time.Time }

// T wraps t as a Time, truncated to microseconds in UTC.
func T(t time.Time) Time {
	return Time{t.UTC().Truncate(time.Microsecond)}
}

// Now returns the current time as a Time.
func Now() Time { return T(time.Now()) }

// Value implements driver.Valuer.
func (t Time) Value() (driver.Value, error) {
	if t.IsZero() {
		return nil, fmt.Errorf("store: zero Time; use *Time for nullable columns")
	}
	return t.UTC().Truncate(time.Microsecond).Format(timeLayout), nil
}

// Scan implements sql.Scanner.
func (t *Time) Scan(src any) error {
	switch v := src.(type) {
	case time.Time:
		*t = T(v)
		return nil
	case string:
		return t.parse(v)
	case []byte:
		return t.parse(string(v))
	default:
		return fmt.Errorf("store: cannot scan %T into Time", src)
	}
}

func (t *Time) parse(s string) error {
	p, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return fmt.Errorf("store: parse time %q: %w", s, err)
	}
	*t = T(p)
	return nil
}

// Date is a calendar date without time zone, stored as TEXT 'YYYY-MM-DD' on
// SQLite and date on Postgres.
type Date struct {
	Year  int
	Month time.Month
	Day   int
}

// DateOf returns the calendar date of t in t's location.
func DateOf(t time.Time) Date {
	y, m, d := t.Date()
	return Date{y, m, d}
}

func (d Date) String() string {
	return fmt.Sprintf("%04d-%02d-%02d", d.Year, d.Month, d.Day)
}

// Value implements driver.Valuer.
func (d Date) Value() (driver.Value, error) {
	if d == (Date{}) {
		return nil, fmt.Errorf("store: zero Date")
	}
	return d.String(), nil
}

// Scan implements sql.Scanner.
func (d *Date) Scan(src any) error {
	switch v := src.(type) {
	case time.Time:
		*d = Date{v.Year(), v.Month(), v.Day()}
		return nil
	case string:
		return d.parse(v)
	case []byte:
		return d.parse(string(v))
	default:
		return fmt.Errorf("store: cannot scan %T into Date", src)
	}
}

func (d *Date) parse(s string) error {
	p, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return fmt.Errorf("store: parse date %q: %w", s, err)
	}
	*d = DateOf(p)
	return nil
}
