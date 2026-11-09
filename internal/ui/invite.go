package ui

import (
	"context"

	"github.com/raphi011/mise/internal/i18n"
)

// inviteEmailHint explains what the invite's email address is for.
func inviteEmailHint(ctx context.Context, canEmail bool) string {
	if canEmail {
		return i18n.T(ctx, "invite.emailHintSend")
	}
	return i18n.T(ctx, "invite.emailHint")
}
