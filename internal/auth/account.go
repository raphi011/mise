package auth

import (
	"context"
	"strings"

	"github.com/raphi011/mise/internal/store"
)

// Profile is the editable part of the signed-in user's account.
type Profile struct {
	Name        string
	Email       string
	AvatarColor string
	// CurrentPassword confirms a change of email address: whoever holds a
	// session could otherwise point the address at themselves and take the
	// account over with a password reset or an SSO link. Other changes do
	// not need it.
	CurrentPassword string
}

// EmailChange is a changed email address, for telling the old one.
type EmailChange struct {
	// Old is the previous address; New the address now stored, empty when
	// it was removed.
	Old, New string
}

// UpdateProfile saves the user's name, email and avatar colour. Changing
// the email address, other than in case, takes the current password. When
// the address changed from a non-empty one, it returns the change.
func (s *Service) UpdateProfile(ctx context.Context, id *Identity, p Profile) (*EmailChange, error) {
	p.Name, p.Email = clean(p.Name), clean(p.Email)
	errs := FieldErrors{}
	checkName(errs, "name", p.Name)
	checkEmail(errs, "email", p.Email)
	checkChoice(errs, "avatar_color", p.AvatarColor, AvatarColors)
	if err := errs.orNil(); err != nil {
		return nil, err
	}
	var stored struct {
		Email        *string
		PasswordHash string
	}
	if err := s.db.System().Read().Get(ctx, &stored, `SELECT email, password_hash FROM users WHERE id = ?`, id.UserID); err != nil {
		return nil, err
	}
	old := deref(stored.Email)
	// Addresses match case-insensitively, as in RequestPasswordReset.
	changed := !strings.EqualFold(old, p.Email)
	if changed {
		if err := confirmPassword(stored.PasswordHash, p.CurrentPassword); err != nil {
			return nil, err
		}
	}
	err := s.db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
		// Reset links went to the old address; they must not outlive it.
		if changed {
			if err := dropResets(ctx, q, id.UserID); err != nil {
				return err
			}
		}
		_, err := q.Exec(ctx, `UPDATE users SET name = ?, email = ?, avatar_color = ?, updated_at = ? WHERE id = ?`,
			p.Name, nullable(p.Email), p.AvatarColor, s.clock(), id.UserID)
		return err
	})
	if err != nil {
		return nil, err
	}
	if !changed || old == "" {
		return nil, nil
	}
	return &EmailChange{Old: old, New: p.Email}, nil
}

// confirmPassword checks the current password a sensitive change needs,
// returning a FieldErrors for the current_password field when it is
// missing or wrong.
func confirmPassword(hash, password string) error {
	if password == "" {
		return FieldErrors{"current_password": "form.error.passwordToChangeEmail"}
	}
	ok, err := VerifyPassword(hash, password)
	if err != nil {
		return err
	}
	if !ok {
		return FieldErrors{"current_password": "form.error.currentPassword"}
	}
	return nil
}

// Preferences are the user's display settings.
type Preferences struct {
	// Theme is one of Themes.
	Theme string
	// Language is a code from Languages, or empty for the household default.
	Language string
	// Units is one of UnitSystems.
	Units string
}

// UpdatePreferences saves the user's theme, language and units.
func (s *Service) UpdatePreferences(ctx context.Context, id *Identity, p Preferences) error {
	errs := FieldErrors{}
	checkChoice(errs, "theme", p.Theme, Themes)
	checkChoice(errs, "units", p.Units, UnitSystems)
	if p.Language != "" {
		checkChoice(errs, "language", p.Language, Languages())
	}
	if err := errs.orNil(); err != nil {
		return err
	}
	return s.db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
		_, err := q.Exec(ctx, `UPDATE users SET theme = ?, language = ?, units = ?, updated_at = ? WHERE id = ?`,
			p.Theme, nullable(p.Language), p.Units, s.clock(), id.UserID)
		return err
	})
}

// ChangePassword replaces the user's password after checking the current
// one, and signs out every other session of the user.
func (s *Service) ChangePassword(ctx context.Context, id *Identity, current, next string) error {
	errs := FieldErrors{}
	if current == "" {
		errs["current_password"] = "form.error.required"
	}
	checkNewPassword(errs, "new_password", next)
	if err := errs.orNil(); err != nil {
		return err
	}
	var stored string
	if err := s.db.System().Read().Get(ctx, &stored, `SELECT password_hash FROM users WHERE id = ?`, id.UserID); err != nil {
		return err
	}
	ok, err := VerifyPassword(stored, current)
	if err != nil {
		return err
	}
	if !ok {
		return FieldErrors{"current_password": "form.error.currentPassword"}
	}
	hash, err := HashPassword(next)
	if err != nil {
		return err
	}
	return s.db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
		if _, err := q.Exec(ctx, `UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`, hash, s.clock(), id.UserID); err != nil {
			return err
		}
		if err := dropResets(ctx, q, id.UserID); err != nil {
			return err
		}
		_, err := q.Exec(ctx, `DELETE FROM sessions WHERE user_id = ? AND id <> ?`, id.UserID, id.SessionID)
		return err
	})
}
