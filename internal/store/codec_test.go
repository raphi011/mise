package store

import (
	"testing"
	"time"
)

func TestDateArithmetic(t *testing.T) {
	d, err := ParseDate("2026-12-30")
	if err != nil {
		t.Fatal(err)
	}
	if got := d.AddDays(3).String(); got != "2027-01-02" {
		t.Errorf("AddDays(3) = %s", got)
	}
	if got := d.AddDays(-30).String(); got != "2026-11-30" {
		t.Errorf("AddDays(-30) = %s", got)
	}
	if got := d.Weekday(); got != time.Wednesday {
		t.Errorf("Weekday = %s", got)
	}
	// Days across a DST change stay calendar days.
	vienna, err := time.LoadLocation("Europe/Vienna")
	if err != nil {
		t.Fatal(err)
	}
	mar, _ := ParseDate("2026-03-28")
	if got := mar.AddDays(2); got.String() != "2026-03-30" || got.In(vienna).Hour() != 0 {
		t.Errorf("AddDays across DST = %s (%v)", got, got.In(vienna))
	}
	if !d.Before(d.AddDays(1)) || d.Compare(d) != 0 || d.AddDays(1).Compare(d) != 1 {
		t.Error("Compare")
	}
	if _, err := ParseDate("2026-02-30"); err == nil {
		t.Error("ParseDate accepted 30 February")
	}
	if !(Date{}).IsZero() || d.IsZero() {
		t.Error("IsZero")
	}
}
