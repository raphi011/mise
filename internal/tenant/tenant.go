// Package tenant carries the active household and user through a request
// context. The auth middleware sets them; store.DB.Scope reads them.
package tenant

import (
	"context"

	"github.com/google/uuid"
)

type ctxKey int

const (
	householdKey ctxKey = iota
	userKey
)

// WithHousehold returns a context carrying the active household.
func WithHousehold(ctx context.Context, id uuid.UUID) context.Context {
	return context.WithValue(ctx, householdKey, id)
}

// HouseholdID returns the active household, if any.
func HouseholdID(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(householdKey).(uuid.UUID)
	return id, ok && id != uuid.Nil
}

// WithUser returns a context carrying the signed-in user.
func WithUser(ctx context.Context, id uuid.UUID) context.Context {
	return context.WithValue(ctx, userKey, id)
}

// UserID returns the signed-in user, if any.
func UserID(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(userKey).(uuid.UUID)
	return id, ok && id != uuid.Nil
}
