package web

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/auth"
	"github.com/raphi011/mise/internal/i18n"
	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/ui"
)

// inviteCookie carries a just-created invite token from the POST to the
// page that shows the link once (post/redirect/get without storing the
// token in the database).
const inviteCookie = "mise_new_invite"

// notices maps the ?done= parameter of a redirect to its status message.
var notices = map[string]i18n.Key{
	"saved":           "notice.saved",
	"password":        "notice.passwordChanged",
	"revoked":         "notice.deviceSignedOut",
	"revokedOthers":   "notice.othersSignedOut",
	"inviteRevoked":   "notice.inviteRevoked",
	"role":            "notice.roleChanged",
	"reset":           "notice.passwordSet",
	"removed":         "notice.memberRemoved",
	"listCreated":     "notice.listCreated",
	"listDeleted":     "notice.listDeleted",
	"listRestored":    "notice.listRestored",
	"categoryDeleted": "notice.categoryDeleted",
}

func noticeFor(r *http.Request) string {
	if key, ok := notices[r.URL.Query().Get("done")]; ok {
		return i18n.T(r.Context(), key)
	}
	return ""
}

// identity returns the signed-in user; routes wrapped in user/admin always
// have one.
func identity(r *http.Request) *auth.Identity {
	id, _ := auth.FromContext(r.Context())
	return id
}

// location returns the household's time zone for displaying times.
func location(id *auth.Identity) *time.Location {
	if loc, err := time.LoadLocation(id.Timezone); err == nil {
		return loc
	}
	return time.UTC
}

func roleLabel(r *http.Request, role auth.Role) string {
	if role == auth.RoleAdmin {
		return i18n.T(r.Context(), "role.admin")
	}
	return i18n.T(r.Context(), "role.member")
}

func roleOptions(r *http.Request) []ui.Option {
	ctx := r.Context()
	return []ui.Option{
		{Value: string(auth.RoleMember), Label: i18n.T(ctx, "role.member"), Hint: i18n.T(ctx, "role.memberHint")},
		{Value: string(auth.RoleAdmin), Label: i18n.T(ctx, "role.admin"), Hint: i18n.T(ctx, "role.adminHint")},
	}
}

// ---- Overview ----

func (s *Server) settingsPage(w http.ResponseWriter, r *http.Request) {
	s.renderSettings(w, r, http.StatusOK, settingsState{notice: noticeFor(r), undoList: undoListParam(r)})
}

// settingsState is what a settings render carries besides stored data:
// submitted forms with errors and the status message.
type settingsState struct {
	prefs ui.Form
	// list is a shopping-list form with errors; listID names its list, or
	// is empty for the new-list form.
	list     ui.Form
	listID   string
	undoList string
	notice   string
}

func (s *Server) renderSettings(w http.ResponseWriter, r *http.Request, status int, st settingsState) {
	ctx := r.Context()
	id := identity(r)
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	members, err := s.auth.Members(ctx, sc)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	lists, err := s.groceries.Lists(ctx, sc)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	cats, err := s.groceries.Categories(ctx, sc)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	v := ui.SettingsView{
		Name: id.Name, Username: id.Username, Color: id.AvatarColor, Admin: id.IsAdmin(),
		Prefs: st.prefs, Notice: st.notice,
		ListForm: st.list, ListFormID: st.listID, UndoList: st.undoList, Categories: len(cats),
		Themes: []ui.Option{
			{Value: "system", Label: i18n.T(ctx, "theme.system")},
			{Value: "light", Label: i18n.T(ctx, "theme.light")},
			{Value: "dark", Label: i18n.T(ctx, "theme.dark")},
		},
		Languages: languageOptions(r),
	}
	for _, l := range lists {
		v.Lists = append(v.Lists, ui.ListTab{ID: l.ID.String(), Name: l.Name, Open: l.Open})
	}
	if v.Prefs.Values == nil {
		v.Prefs.Values = map[string]string{"theme": id.Theme, "language": id.Language}
	}
	for _, m := range members {
		row := ui.MemberRow{Name: m.Name, Color: m.AvatarColor, Role: roleLabel(r, m.Role), You: m.UserID == id.UserID}
		if id.IsAdmin() {
			row.Href = "/settings/people/" + m.UserID.String()
		}
		v.Members = append(v.Members, row)
	}
	if id.IsAdmin() {
		invites, err := s.auth.PendingInvites(ctx, sc)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		for _, inv := range invites {
			label := inv.Email
			if label == "" {
				label = i18n.T(ctx, "settings.inviteLink")
			}
			v.Invites = append(v.Invites, ui.InviteRow{
				ID:    inv.ID.String(),
				Label: label,
				Note:  i18n.T(ctx, "settings.inviteNote", roleLabel(r, inv.Role), daysLeft(r, inv.ExpiresAt.Sub(s.auth.Now()))),
			})
		}
	}
	s.render(w, r, status, ui.Settings(s.page(r, i18n.T(ctx, "page.settings.title"), ui.NavSettings), v))
}

// languageOptions lists the household default and every supported UI
// language, each named in its own language.
func languageOptions(r *http.Request) []ui.Option {
	opts := []ui.Option{{Value: "", Label: i18n.T(r.Context(), "language.householdDefault")}}
	for _, tag := range i18n.Supported {
		opts = append(opts, ui.Option{Value: tag.String(), Label: i18n.T(i18n.WithLocale(r.Context(), tag), "language.self")})
	}
	return opts
}

// daysLeft describes how long an invite with the given validity left stays
// valid.
func daysLeft(r *http.Request, left time.Duration) string {
	days := int(left.Hours() / 24)
	if days < 1 {
		return i18n.T(r.Context(), "settings.expiresToday")
	}
	return i18n.T(r.Context(), "settings.expiresDays", days)
}

func (s *Server) savePreferences(w http.ResponseWriter, r *http.Request) {
	f := formOf(r, "theme", "language")
	err := s.auth.UpdatePreferences(r.Context(), identity(r), auth.Preferences{Theme: f.Value("theme"), Language: f.Value("language")})
	if f, ok := withErrors(r.Context(), f, err); ok {
		s.renderSettings(w, r, http.StatusUnprocessableEntity, settingsState{prefs: f})
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	seeOther(w, r, "/settings?done=saved#app")
}

// ---- Account ----

func (s *Server) accountPage(w http.ResponseWriter, r *http.Request) {
	id := identity(r)
	profile := ui.Form{Values: map[string]string{"name": id.Name, "email": id.Email, "avatar_color": id.AvatarColor}}
	s.renderAccount(w, r, http.StatusOK, profile, ui.Form{}, noticeFor(r))
}

func (s *Server) renderAccount(w http.ResponseWriter, r *http.Request, status int, profile, password ui.Form, notice string) {
	ctx := r.Context()
	id := identity(r)
	sessions, err := s.auth.Sessions(ctx, id)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	v := ui.AccountView{
		Name: id.Name, Username: id.Username, Color: id.AvatarColor,
		Profile: profile, Password: password, PasswordOpen: password.HasErrors(), Notice: notice,
	}
	for _, c := range auth.AvatarColors {
		v.Colors = append(v.Colors, ui.Option{Value: c, Label: colorLabel(r, c)})
	}
	loc := location(id)
	for _, sess := range sessions {
		v.Sessions = append(v.Sessions, ui.SessionRow{
			ID:       sess.ID,
			Device:   deviceLabel(r, sess.UserAgent),
			LastSeen: sess.LastSeenAt.In(loc).Format("2006-01-02 15:04"),
			Current:  sess.Current,
		})
	}
	p := s.page(r, i18n.T(ctx, "account.title"), ui.NavSettings)
	p.BackHref, p.BackLabel = "/settings", i18n.T(ctx, "settings.back")
	s.render(w, r, status, ui.Account(p, v))
}

func colorLabel(r *http.Request, c string) string {
	ctx := r.Context()
	switch c {
	case "amber":
		return i18n.T(ctx, "color.amber")
	case "sky":
		return i18n.T(ctx, "color.sky")
	case "sand":
		return i18n.T(ctx, "color.sand")
	case "herb":
		return i18n.T(ctx, "color.herb")
	case "coral":
		return i18n.T(ctx, "color.coral")
	case "plum":
		return i18n.T(ctx, "color.plum")
	}
	return c
}

func (s *Server) saveProfile(w http.ResponseWriter, r *http.Request) {
	f := formOf(r, "name", "email", "avatar_color")
	err := s.auth.UpdateProfile(r.Context(), identity(r), auth.Profile{Name: f.Value("name"), Email: f.Value("email"), AvatarColor: f.Value("avatar_color")})
	if f, ok := withErrors(r.Context(), f, err); ok {
		s.renderAccount(w, r, http.StatusUnprocessableEntity, f, ui.Form{}, "")
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	seeOther(w, r, "/settings/account?done=saved")
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := identity(r)
	profile := ui.Form{Values: map[string]string{"name": id.Name, "email": id.Email, "avatar_color": id.AvatarColor}}
	pw := ui.Form{Values: map[string]string{}, Errors: map[string]string{}}
	// The current-password check is a password oracle for whoever holds the
	// session; limit it per user, apart from the sign-in buckets.
	attempts := []attempt{{s.passwordLimit, id.UserID.String()}}
	if !allowAll(attempts) {
		pw.Alert = i18n.T(ctx, "signin.error.tooMany")
		s.renderAccount(w, r, http.StatusTooManyRequests, profile, pw, "")
		return
	}
	err := s.auth.ChangePassword(ctx, id, r.PostFormValue("current_password"), r.PostFormValue("new_password"))
	if pw, ok := withErrors(ctx, pw, err); ok {
		s.renderAccount(w, r, http.StatusUnprocessableEntity, profile, pw, "")
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	refundAll(attempts)
	s.sessionsEnded(id.UserID)
	seeOther(w, r, "/settings/account?done=password")
}

func (s *Server) revokeSession(w http.ResponseWriter, r *http.Request) {
	id := identity(r)
	sessionID := r.PathValue("id")
	err := s.auth.RevokeSession(r.Context(), id, sessionID)
	if errors.Is(err, auth.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.sessionsEnded(id.UserID)
	if sessionID == id.SessionID {
		s.auth.ClearSessionCookie(w, r)
		seeOther(w, r, "/signin")
		return
	}
	seeOther(w, r, "/settings/account?done=revoked")
}

func (s *Server) revokeOtherSessions(w http.ResponseWriter, r *http.Request) {
	if err := s.auth.RevokeOtherSessions(r.Context(), identity(r)); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.sessionsEnded(identity(r).UserID)
	seeOther(w, r, "/settings/account?done=revokedOthers")
}

// deviceLabel describes a user agent as "Browser on OS".
func deviceLabel(r *http.Request, ua string) string {
	browser := ""
	switch {
	case strings.Contains(ua, "Edg/"):
		browser = "Edge"
	case strings.Contains(ua, "Firefox/"):
		browser = "Firefox"
	case strings.Contains(ua, "Chrome/") || strings.Contains(ua, "CriOS/"):
		browser = "Chrome"
	case strings.Contains(ua, "Safari/"):
		browser = "Safari"
	}
	os := ""
	switch {
	case strings.Contains(ua, "iPhone"):
		os = "iPhone"
	case strings.Contains(ua, "iPad"):
		os = "iPad"
	case strings.Contains(ua, "Android"):
		os = "Android"
	case strings.Contains(ua, "Mac OS X"):
		os = "macOS"
	case strings.Contains(ua, "Windows"):
		os = "Windows"
	case strings.Contains(ua, "Linux"):
		os = "Linux"
	}
	switch {
	case browser != "" && os != "":
		return i18n.T(r.Context(), "account.device", browser, os)
	case browser != "":
		return browser
	case os != "":
		return os
	}
	return i18n.T(r.Context(), "account.unknownDevice")
}

// ---- Invites ----

func (s *Server) invitePage(w http.ResponseWriter, r *http.Request) {
	v := ui.InvitePageView{Roles: roleOptions(r), Form: ui.Form{Values: map[string]string{"role": "member"}}}
	if c, err := r.Cookie(inviteCookie); err == nil && c.Value != "" {
		if base := s.BaseURL(); base != nil {
			v.Link = base.String() + "/invite/" + c.Value
		}
		s.setInviteCookie(w, r, "", -1)
	}
	s.renderInvitePage(w, r, http.StatusOK, v)
}

func (s *Server) renderInvitePage(w http.ResponseWriter, r *http.Request, status int, v ui.InvitePageView) {
	p := s.page(r, i18n.T(r.Context(), "invite.title"), ui.NavSettings)
	p.BackHref, p.BackLabel = "/settings", i18n.T(r.Context(), "settings.back")
	// The page may show a secret link.
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, r, status, ui.InvitePage(p, v))
}

func (s *Server) setInviteCookie(w http.ResponseWriter, r *http.Request, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: inviteCookie, Value: value, Path: "/settings/invite", MaxAge: maxAge,
		HttpOnly: true, Secure: s.secureCookie(r), SameSite: http.SameSiteStrictMode,
	})
}

func (s *Server) createInvite(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f := formOf(r, "email", "role")
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	token, err := s.auth.CreateInvite(ctx, sc, identity(r).UserID, auth.Role(f.Value("role")), f.Value("email"))
	if f, ok := withErrors(ctx, f, err); ok {
		s.renderInvitePage(w, r, http.StatusUnprocessableEntity, ui.InvitePageView{Form: f, Roles: roleOptions(r)})
		return
	}
	if err != nil {
		s.adminError(w, r, err)
		return
	}
	s.setInviteCookie(w, r, token, 600)
	seeOther(w, r, "/settings/invite")
}

func (s *Server) revokeInvite(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	inviteID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.notFound(w, r)
		return
	}
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.auth.RevokeInvite(ctx, sc, identity(r).UserID, inviteID); err != nil {
		s.adminError(w, r, err)
		return
	}
	seeOther(w, r, "/settings?done=inviteRevoked#people")
}

// adminError answers errors from admin operations that have no form to
// show them in.
func (s *Server) adminError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, auth.ErrNotFound):
		s.notFound(w, r)
	case errors.Is(err, auth.ErrForbidden):
		s.forbidden(w, r)
	default:
		s.serverError(w, r, err)
	}
}

// ---- Members ----

type memberForms struct {
	role, password, remove ui.Form
}

func (s *Server) memberPage(w http.ResponseWriter, r *http.Request) {
	s.renderMember(w, r, http.StatusOK, memberForms{}, noticeFor(r))
}

func (s *Server) renderMember(w http.ResponseWriter, r *http.Request, status int, forms memberForms, notice string) {
	ctx := r.Context()
	id := identity(r)
	userID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.notFound(w, r)
		return
	}
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	m, err := s.auth.Member(ctx, sc, userID)
	if err != nil {
		s.adminError(w, r, err)
		return
	}
	if forms.role.Values == nil {
		forms.role.Values = map[string]string{"role": string(m.Role)}
	}
	v := ui.MemberPageView{
		ID: m.UserID.String(), Name: m.Name, Username: m.Username, Color: m.AvatarColor,
		Joined: i18n.T(ctx, "member.joined", m.JoinedAt.In(location(id)).Format("2006-01-02")),
		You:    m.UserID == id.UserID, Roles: roleOptions(r),
		RoleForm: forms.role, Password: forms.password, Remove: forms.remove, Notice: notice,
	}
	p := s.page(r, m.Name, ui.NavSettings)
	p.BackHref, p.BackLabel = "/settings#people", i18n.T(ctx, "settings.back")
	s.render(w, r, status, ui.MemberPage(p, v))
}

// memberAction parses the member ID and scope shared by the member routes.
func (s *Server) memberAction(w http.ResponseWriter, r *http.Request) (store.Scoped, uuid.UUID, bool) {
	userID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.notFound(w, r)
		return store.Scoped{}, uuid.Nil, false
	}
	sc, err := s.db.Scope(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return store.Scoped{}, uuid.Nil, false
	}
	return sc, userID, true
}

// memberFormError re-renders the member page with err in form, or reports
// false when err belongs to no form.
func (s *Server) memberFormError(w http.ResponseWriter, r *http.Request, err error, f ui.Form, place func(*memberForms, ui.Form)) bool {
	ctx := r.Context()
	if f2, ok := withErrors(ctx, f, err); ok {
		var forms memberForms
		place(&forms, f2)
		s.renderMember(w, r, http.StatusUnprocessableEntity, forms, "")
		return true
	}
	var alert i18n.Key
	switch {
	case errors.Is(err, auth.ErrLastAdmin):
		alert = "member.error.lastAdmin"
	case errors.Is(err, auth.ErrSelf):
		alert = "member.error.self"
	case errors.Is(err, auth.ErrSharedAccount):
		alert = "member.error.sharedAccount"
	default:
		return false
	}
	f.Alert = i18n.T(ctx, alert)
	var forms memberForms
	place(&forms, f)
	s.renderMember(w, r, http.StatusConflict, forms, "")
	return true
}

func (s *Server) setRole(w http.ResponseWriter, r *http.Request) {
	sc, userID, ok := s.memberAction(w, r)
	if !ok {
		return
	}
	f := formOf(r, "role")
	err := s.auth.SetRole(r.Context(), sc, identity(r).UserID, userID, auth.Role(f.Value("role")))
	if s.memberFormError(w, r, err, f, func(m *memberForms, f ui.Form) { m.role = f }) {
		return
	}
	if err != nil {
		s.adminError(w, r, err)
		return
	}
	if userID == identity(r).UserID && auth.Role(f.Value("role")) != auth.RoleAdmin {
		// Stepped down: the member page is admin-only now.
		seeOther(w, r, "/settings?done=role#people")
		return
	}
	seeOther(w, r, "/settings/people/"+userID.String()+"?done=role")
}

func (s *Server) resetPassword(w http.ResponseWriter, r *http.Request) {
	sc, userID, ok := s.memberAction(w, r)
	if !ok {
		return
	}
	f := ui.Form{Values: map[string]string{}}
	err := s.auth.ResetPassword(r.Context(), sc, identity(r).UserID, userID, r.PostFormValue("password"))
	if s.memberFormError(w, r, err, f, func(m *memberForms, f ui.Form) { m.password = f }) {
		return
	}
	if err != nil {
		s.adminError(w, r, err)
		return
	}
	s.sessionsEnded(userID)
	seeOther(w, r, "/settings/people/"+userID.String()+"?done=reset")
}

func (s *Server) removeMember(w http.ResponseWriter, r *http.Request) {
	sc, userID, ok := s.memberAction(w, r)
	if !ok {
		return
	}
	if r.PostFormValue("confirm") != "yes" {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	err := s.auth.RemoveMember(r.Context(), sc, identity(r).UserID, userID)
	if s.memberFormError(w, r, err, ui.Form{}, func(m *memberForms, f ui.Form) { m.remove = f }) {
		return
	}
	if err != nil {
		s.adminError(w, r, err)
		return
	}
	s.sessionsEnded(userID)
	seeOther(w, r, "/settings?done=removed#people")
}
