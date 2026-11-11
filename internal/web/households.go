package web

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/auth"
	"github.com/raphi011/mise/internal/i18n"
	"github.com/raphi011/mise/internal/ui"
)

const householdsPath = "/settings/households"

func roleKey(r auth.Role) i18n.Key {
	if r == auth.RoleAdmin {
		return "role.admin"
	}
	return "role.member"
}

// householdItems turns the user's households into view rows, marking the
// active one.
func householdItems(ctx context.Context, hs []auth.HouseholdInfo, active uuid.UUID) []ui.HouseholdItem {
	items := make([]ui.HouseholdItem, len(hs))
	for i, h := range hs {
		items[i] = ui.HouseholdItem{ID: h.ID.String(), Name: h.Name, Role: i18n.T(ctx, roleKey(h.Role)), Current: h.ID == active}
	}
	return items
}

// switcher is the household selector of a page, or nil for users with fewer
// than two households. It runs once per page render, not per request.
func (s *Server) switcher(r *http.Request, id *auth.Identity) *ui.HouseholdSwitcher {
	ctx := r.Context()
	hs, err := s.auth.Households(ctx, id.UserID)
	if err != nil {
		s.log.WarnContext(ctx, "list households", slog.Any("err", err))
		return nil
	}
	if len(hs) < 2 {
		return nil
	}
	sw := &ui.HouseholdSwitcher{Items: householdItems(ctx, hs, id.HouseholdID)}
	for _, h := range sw.Items {
		if h.Current {
			sw.Current = h
		}
	}
	return sw
}

func (s *Server) householdsPage(w http.ResponseWriter, r *http.Request) {
	s.renderHouseholds(w, r, http.StatusOK, ui.HouseholdsView{Notice: noticeFor(r)})
}

func (s *Server) renderHouseholds(w http.ResponseWriter, r *http.Request, status int, v ui.HouseholdsView) {
	ctx := r.Context()
	id := identity(r)
	hs, err := s.auth.Households(ctx, id.UserID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	v.Items = householdItems(ctx, hs, id.HouseholdID)
	p := s.page(r, i18n.T(ctx, "households.title"), ui.NavSettings)
	p.BackHref, p.BackLabel, p.Settings = "/settings/account", i18n.T(ctx, "account.back"), "account"
	s.render(w, r, status, ui.Households(p, v))
}

// householdID parses the {id} path value.
func householdID(r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	return id, err == nil
}

func (s *Server) switchHousehold(w http.ResponseWriter, r *http.Request) {
	hid, ok := householdID(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	err := s.auth.SwitchHousehold(r.Context(), identity(r), hid)
	if errors.Is(err, auth.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	// Never back to the page left: its IDs belong to the old household.
	seeOther(w, r, "/recipes")
}

func (s *Server) leaveHousehold(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	hid, ok := householdID(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	id := identity(r)
	_, err := s.auth.LeaveHousehold(ctx, id, hid)
	refuse := func(msg i18n.Key) {
		s.renderHouseholds(w, r, http.StatusConflict, ui.HouseholdsView{LeaveID: hid.String(), LeaveError: i18n.T(ctx, msg)})
	}
	switch {
	case errors.Is(err, auth.ErrNotFound):
		s.notFound(w, r)
		return
	case errors.Is(err, auth.ErrLastAdmin):
		refuse("households.error.lastAdmin")
		return
	case errors.Is(err, auth.ErrLastHousehold):
		refuse("households.error.lastHousehold")
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	// The user's other devices in that household are signed out.
	s.sessionsEnded(id.UserID)
	seeOther(w, r, householdsPath+"?done=left")
}

// inviteToken extracts the token from a pasted invite link, or takes the
// input as the bare token.
func inviteToken(input string) string {
	input = strings.TrimSpace(input)
	if u, err := url.Parse(input); err == nil && u.Path != "" {
		if _, tok, ok := strings.Cut(u.Path, "/invite/"); ok {
			return strings.Trim(tok, "/")
		}
	}
	return input
}

// joinByLink sends a pasted invite link to its accept page, which shows
// what is being joined and asks for confirmation.
func (s *Server) joinByLink(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f := formOf(r, "link")
	tok := inviteToken(f.Value("link"))
	if tok == "" || strings.ContainsAny(tok, "/?#") {
		f.Errors["link"] = i18n.T(ctx, "households.error.link")
		s.renderHouseholds(w, r, http.StatusUnprocessableEntity, ui.HouseholdsView{Form: f})
		return
	}
	seeOther(w, r, "/invite/"+url.PathEscape(tok))
}
