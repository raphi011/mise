package web

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/config"
	"github.com/raphi011/mise/internal/store/storetest"
)

// firstList returns the ID of the list /groceries shows.
func firstList(t *testing.T, b *browser) string {
	t.Helper()
	m := liveListRe.FindStringSubmatch(b.get("/groceries").expect(t, "groceries", http.StatusOK).body)
	if m == nil {
		t.Fatal("no live stream on the groceries page")
	}
	return m[1]
}

// streamEnds fails unless s ends within the live deadline.
func streamEnds(t *testing.T, what string, s *stream) {
	t.Helper()
	select {
	case <-s.done:
	case <-time.After(liveDeadline):
		t.Errorf("%s: stream still open", what)
	}
}

// TestLiveOtherListCounts checks that the open counts on the list tabs stay
// live for lists other than the one shown.
func TestLiveOtherListCounts(t *testing.T) {
	_, srv := startTimedServer(t, storetest.SQLite(t))
	anna, ben := household(t, srv)
	first := firstList(t, anna)
	anna.post("/settings/lists", url.Values{"name": {"Drugstore"}}).expect(t, "create list", http.StatusSeeOther)
	var second string
	for _, m := range listHrefRe.FindAllStringSubmatch(anna.get("/groceries").body, -1) {
		if m[1] != first {
			second = m[1]
		}
	}
	if second == "" {
		t.Fatal("second list not shown")
	}

	benLive := ben.openStream(first)
	benLive.next("initial", liveDeadline, `id="groceries-live"`, "Drugstore")
	anna.post("/groceries/lists/"+second+"/entries", url.Values{"text": {"soap"}}).expect(t, "add to other list", http.StatusSeeOther)
	benLive.next("count of the other list", liveDeadline, `class="chip__count">1</span>`)
}

// TestLiveStreamEndsWithAccess checks that removing a member or revoking a
// session from another device ends that viewer's open streams.
func TestLiveStreamEndsWithAccess(t *testing.T) {
	_, srv := startTimedServer(t, storetest.SQLite(t))
	anna, ben := household(t, srv)
	list := firstList(t, anna)

	// Revoked from Ben's other device.
	phone := newBrowser(t, srv.URL)
	phone.post("/signin", url.Values{"username": {"ben"}, "password": {memberPassword}}).expect(t, "phone sign-in", http.StatusSeeOther)
	phoneLive := phone.openStream(list)
	phoneLive.next("phone initial", liveDeadline, `id="groceries-live"`)
	ben.post("/settings/account/sessions/revoke-others", nil).expect(t, "revoke others", http.StatusSeeOther)
	streamEnds(t, "revoked session", phoneLive)

	// Removed by an admin.
	benLive := ben.openStream(list)
	benLive.next("ben initial", liveDeadline, `id="groceries-live"`)
	var benID string
	for _, m := range memberHrefRe.FindAllStringSubmatch(anna.get("/settings").body, -1) {
		if strings.Contains(anna.get("/settings/people/"+m[1]).body, "@ben") {
			benID = m[1]
		}
	}
	if benID == "" {
		t.Fatal("Ben not found among members")
	}
	anna.post("/settings/people/"+benID+"/remove", url.Values{"confirm": {"yes"}}).expect(t, "remove ben", http.StatusSeeOther)
	streamEnds(t, "removed member", benLive)
}

// TestLiveGoneList checks that a stream for a deleted list, or a list of
// another household, shows the viewer's own tabs and the deleted state.
func TestLiveGoneList(t *testing.T) {
	_, srv := startTimedServer(t, storetest.SQLite(t))
	anna, _ := household(t, srv)
	firstList(t, anna)
	anna.post("/settings/lists", url.Values{"name": {"Party"}}).expect(t, "create list", http.StatusSeeOther)
	var party string
	for _, m := range listHrefRe.FindAllStringSubmatch(anna.get("/groceries").body, -1) {
		party = m[1]
	}
	anna.post("/settings/lists/"+party+"/delete", nil).expect(t, "delete list", http.StatusSeeOther)

	for _, id := range []string{party, uuid.NewString()} {
		s := anna.openStream(id)
		ev := s.next("gone list "+id, liveDeadline, `id="groceries-live"`, "This list was deleted")
		if strings.Contains(ev, "Party") {
			t.Errorf("deleted list still in the tabs:\n%s", ev)
		}
	}
}

// TestShutdownEndsStreams checks that graceful shutdown does not wait for
// open live streams.
func TestShutdownEndsStreams(t *testing.T) {
	s := newServer(t, config.Config{}, storetest.SQLite(t), slog.New(slog.DiscardHandler))
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: time.Second}
	srv.RegisterOnShutdown(s.CloseStreams)
	go func() { _ = srv.Serve(ln) }()
	base := "http://" + ln.Addr().String()

	anna := newBrowser(t, base)
	anna.post("/setup", setupForm(base)).expect(t, "setup", http.StatusSeeOther)
	live := anna.openStream(firstList(t, anna))
	live.next("initial", liveDeadline, `id="groceries-live"`)

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	start := time.Now()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown with an open stream: %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("shutdown took %s", d)
	}
	streamEnds(t, "after shutdown", live)
}
