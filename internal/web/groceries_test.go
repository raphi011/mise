package web

import (
	"bufio"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/raphi011/mise/internal/config"
	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
)

var (
	liveListRe = regexp.MustCompile(`/live\?page=groceries&amp;list=([0-9a-f-]{36})`)
	entryRe    = regexp.MustCompile(`/groceries/entries/([0-9a-f-]{36})/check`)
	listHrefRe = regexp.MustCompile(`href="/groceries/lists/([0-9a-f-]{36})"`)
	categoryRe = regexp.MustCompile(`action="/settings/categories/([0-9a-f-]{36})"`)
)

// liveDeadline is how fast a change must reach the other phone.
const liveDeadline = time.Second

// startTimedServer serves live streams with short read and write timeouts,
// which prove that the streams clear their deadlines. Other requests clear
// theirs here: hashing a password under -race can take longer than the
// timeouts, which would cut off setup and sign-in.
func startTimedServer(t *testing.T, db *store.DB) (*Server, *httptest.Server) {
	t.Helper()
	s := newServer(t, config.Config{}, db, slog.New(slog.DiscardHandler))
	s.heartbeat = 100 * time.Millisecond
	h := s.Handler()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/live" {
			rc := http.NewResponseController(w)
			if err := rc.SetReadDeadline(time.Time{}); err != nil {
				t.Error(err)
			}
			if err := rc.SetWriteDeadline(time.Time{}); err != nil {
				t.Error(err)
			}
		}
		h.ServeHTTP(w, r)
	}))
	srv.Config.ReadTimeout = 300 * time.Millisecond
	srv.Config.WriteTimeout = 300 * time.Millisecond
	srv.Start()
	t.Cleanup(srv.Close)
	return s, srv
}

// household sets up an admin and an invited member, each in their own
// browser.
func household(t *testing.T, srv *httptest.Server) (admin, member *browser) {
	t.Helper()
	admin, member = newBrowser(t, srv.URL), newBrowser(t, srv.URL)
	admin.post("/setup", setupForm(srv.URL)).expect(t, "setup", http.StatusSeeOther, "/")
	admin.post("/settings/invites", url.Values{"role": {"member"}}).expect(t, "invite", http.StatusSeeOther)
	m := inviteLinkRe.FindStringSubmatch(admin.get("/settings/invite").body)
	if m == nil {
		t.Fatal("no invite link")
	}
	member.post(m[1], url.Values{"name": {"Ben"}, "username": {"ben"}, "password": {memberPassword}}).
		expect(t, "accept invite", http.StatusSeeOther, "/")
	return admin, member
}

// datastar posts a form the way Datastar does and returns the SSE body.
func (b *browser) datastar(method, path string, form url.Values) page {
	b.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(b.t.Context(), method, b.url(path), body)
	if err != nil {
		b.t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.Header.Set("Datastar-Request", "true")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	return b.do(req)
}

// stream is an open live SSE stream.
type stream struct {
	t      *testing.T
	events chan string
	done   chan struct{}
}

// openStream opens the list page's live stream like Datastar's @get.
func (b *browser) openStream(listID string) *stream {
	b.t.Helper()
	return b.openStreamURL("/live?page=groceries&list=" + listID)
}

// openStreamURL opens a live stream at path like Datastar's @get.
func (b *browser) openStreamURL(path string) *stream {
	b.t.Helper()
	req, err := http.NewRequestWithContext(b.t.Context(), http.MethodGet, b.url(path), nil)
	if err != nil {
		b.t.Fatal(err)
	}
	req.Header.Set("Datastar-Request", "true")
	res, err := b.c.Do(req) //nolint:bodyclose // the reader goroutine and t.Cleanup close it
	if err != nil {
		b.t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "text/event-stream" {
		b.t.Fatalf("live stream: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	s := &stream{t: b.t, events: make(chan string, 64), done: make(chan struct{})}
	go func() {
		defer close(s.done)
		defer func() { _ = res.Body.Close() }()
		sc := bufio.NewScanner(res.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		var ev strings.Builder
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "" && ev.Len() > 0:
				s.events <- ev.String()
				ev.Reset()
			case strings.HasPrefix(line, ": "):
				s.events <- line
			default:
				ev.WriteString(line + "\n")
			}
		}
	}()
	b.t.Cleanup(func() { _ = res.Body.Close() })
	return s
}

// next waits up to d for an event containing all of want and returns it.
func (s *stream) next(what string, d time.Duration, want ...string) string {
	s.t.Helper()
	timeout := time.After(d)
	for {
		select {
		case ev := <-s.events:
			ok := true
			for _, w := range want {
				ok = ok && strings.Contains(ev, w)
			}
			if ok {
				return ev
			}
		case <-s.done:
			s.t.Fatalf("%s: stream ended", what)
		case <-timeout:
			s.t.Fatalf("%s: nothing within %s containing %q", what, d, want)
		}
	}
}

// TestShoppingTogether is P1b's done-criterion: two people shop from the
// same list on two phones and see each other's changes within a second.
func TestShoppingTogether(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		_, srv := startTimedServer(t, db)
		anna, ben := household(t, srv)

		page := anna.get("/groceries").expect(t, "groceries", http.StatusOK)
		m := liveListRe.FindStringSubmatch(page.body)
		if m == nil {
			t.Fatalf("no live stream on the page:\n%.2000s", page.body)
		}
		list := m[1]
		annaLive, benLive := anna.openStream(list), ben.openStream(list)
		annaLive.next("anna initial", liveDeadline, "datastar-patch-elements", `id="groceries-live"`, "All done")
		benLive.next("ben initial", liveDeadline, `id="groceries-live"`)

		// The streams outlive the server's read and write timeouts.
		time.Sleep(500 * time.Millisecond)
		benLive.next("heartbeat", liveDeadline, ": ping")

		// Anna adds lemons; Ben sees them, marked as added by Anna.
		res := anna.datastar(http.MethodPost, "/groceries/lists/"+list+"/entries", url.Values{"text": {"2 lemons"}})
		res.expect(t, "add", http.StatusOK)
		if !strings.Contains(res.body, "datastar-patch-signals") || !strings.Contains(res.body, "Lemons") {
			t.Errorf("add response:\n%s", res.body)
		}
		ev := benLive.next("ben sees lemons", liveDeadline, "Lemons", "Added by Anna")
		entry := entryRe.FindStringSubmatch(ev)
		if entry == nil {
			t.Fatalf("no check-off form for the entry:\n%s", ev)
		}
		annaLive.next("anna sees her own add", liveDeadline, "Lemons")

		// Ben checks them off; Anna sees them in the cart.
		res = ben.datastar(http.MethodPost, "/groceries/entries/"+entry[1]+"/check", url.Values{})
		res.expect(t, "check", http.StatusOK)
		if !strings.Contains(res.body, "Lemons checked off") || !strings.Contains(res.body, "/groceries/entries/"+entry[1]+"/uncheck") {
			t.Errorf("check response has no Undo toast:\n%s", res.body)
		}
		annaLive.next("anna sees the cart", liveDeadline, "In the cart", "Put back: Lemons")

		// Undo puts them back for both.
		ben.datastar(http.MethodPost, "/groceries/entries/"+entry[1]+"/uncheck", url.Values{}).expect(t, "undo", http.StatusOK)
		ev = annaLive.next("anna sees undo", liveDeadline, "Check off: Lemons")
		if strings.Contains(ev, "In the cart") {
			t.Error("cart still shown after undo")
		}

		// Anna checks off and clears the cart; Ben's list offers lemons
		// again under Usually bought.
		anna.post("/groceries/entries/"+entry[1]+"/check", url.Values{}).expect(t, "check without JS", http.StatusSeeOther, "/groceries/lists/"+list)
		benLive.next("ben sees cart", liveDeadline, "In the cart")
		p := anna.get("/groceries/lists/"+list+"/clear").expect(t, "clear page", http.StatusOK)
		if !strings.Contains(p.body, "Clear 1 item") {
			t.Errorf("clear confirmation:\n%.1500s", p.body)
		}
		res = anna.datastar(http.MethodPost, "/groceries/lists/"+list+"/clear", url.Values{})
		if !strings.Contains(res.body, "Cart cleared") || !strings.Contains(res.body, "restore-cart") {
			t.Errorf("clear response has no Undo:\n%s", res.body)
		}
		// The add-to-home-screen hint rides along; app.js decides whether
		// this device sees it.
		if !strings.Contains(res.body, `id="install-hint"`) {
			t.Errorf("clear response has no install hint:\n%s", res.body)
		}
		benLive.next("ben sees usually bought", liveDeadline, "Usually bought", "+ Lemons")

		// A list renamed in settings updates the tabs on both phones.
		anna.post("/settings/lists/"+list+"/rename", url.Values{"name": {"Weekly shop"}}).expect(t, "rename", http.StatusSeeOther)
		benLive.next("ben sees rename", liveDeadline, "Weekly shop")

		// Signing out ends that phone's stream.
		ben.post("/signout", nil).expect(t, "sign out", http.StatusSeeOther, "/signin")
		select {
		case <-benLive.done:
		case <-time.After(liveDeadline):
			t.Error("stream still open after sign-out")
		}
		anna.post("/groceries/lists/"+list+"/entries", url.Values{"text": {"bread"}}).expect(t, "add after sign-out", http.StatusSeeOther)
		annaLive.next("anna still live", liveDeadline, "Bread")
	})
}

func TestGroceriesWithoutJavaScript(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		_, srv := startServer(t, db)
		anna, _ := household(t, srv)
		page := anna.get("/groceries").expect(t, "groceries", http.StatusOK)
		list := liveListRe.FindStringSubmatch(page.body)[1]
		back := "/groceries/lists/" + list

		anna.post(back+"/entries", url.Values{"text": {" "}}).expect(t, "empty add", http.StatusUnprocessableEntity)
		anna.post(back+"/entries", url.Values{"text": {"500 g Tofu"}}).expect(t, "add", http.StatusSeeOther, back)
		anna.post(back+"/entries", url.Values{"text": {"tofu 250g"}}).expect(t, "add again", http.StatusSeeOther, back)
		p := anna.get(back).expect(t, "list", http.StatusOK)
		if !strings.Contains(p.body, "750 g") {
			t.Errorf("amounts not merged:\n%.3000s", p.body)
		}
		id := entryRe.FindStringSubmatch(p.body)[1]

		// Edit sheet as a page: move into a new category.
		anna.get("/groceries/entries/"+id+"/edit").expect(t, "edit page", http.StatusOK)
		anna.post("/groceries/entries/"+id+"/edit", url.Values{"name": {""}, "list": {list}}).
			expect(t, "edit without name", http.StatusUnprocessableEntity)
		anna.post("/groceries/entries/"+id+"/edit", url.Values{
			"name": {"Firm tofu"}, "amount": {"1 pack"}, "new_category": {"Asian"}, "list": {list}, "note": {"smoked"},
		}).expect(t, "save edit", http.StatusSeeOther, back)
		p = anna.get(back)
		for _, want := range []string{"Firm tofu", "1 pack · smoked", ">Asian</h2>"} {
			if !strings.Contains(p.body, want) {
				t.Errorf("list lacks %q after edit", want)
			}
		}

		// Remove and restore.
		anna.post("/groceries/entries/"+id+"/remove", nil).expect(t, "remove", http.StatusSeeOther, back)
		if strings.Contains(anna.get(back).body, "Firm tofu") {
			t.Error("removed entry still listed")
		}
		anna.post("/groceries/entries/"+id+"/restore", nil).expect(t, "restore", http.StatusSeeOther, back)
		anna.post("/groceries/entries/"+id+"/restore", nil).expect(t, "restore twice", http.StatusNotFound)

		// Lists in settings.
		anna.post("/settings/lists", url.Values{"name": {""}}).expect(t, "empty list name", http.StatusUnprocessableEntity)
		anna.post("/settings/lists", url.Values{"name": {"Drugstore"}}).expect(t, "create list", http.StatusSeeOther, "/settings?done=listCreated#lists")
		var drugstore string
		for _, m := range listHrefRe.FindAllStringSubmatch(anna.get("/groceries").body, -1) {
			if m[1] != list {
				drugstore = m[1]
			}
		}
		if drugstore == "" {
			t.Fatal("new list not in the tabs")
		}
		anna.post("/settings/lists/"+drugstore+"/move", url.Values{"to": {"0"}}).expect(t, "move list", http.StatusSeeOther)
		if p := anna.get("/groceries"); !strings.Contains(p.body, `href="/groceries/lists/`+drugstore+`" aria-current="page"`) {
			t.Error("first list is not the default after moving")
		}
		p = anna.post("/settings/lists/"+drugstore+"/delete", nil).expect(t, "delete list", http.StatusSeeOther)
		p = anna.get(p.location)
		if !strings.Contains(p.body, "/settings/lists/"+drugstore+"/restore") {
			t.Error("no undo after deleting a list")
		}
		anna.get("/groceries/lists/"+drugstore).expect(t, "deleted list", http.StatusNotFound)
		anna.post("/settings/lists/"+drugstore+"/restore", nil).expect(t, "restore list", http.StatusSeeOther)
		anna.post("/settings/lists/"+drugstore+"/delete", nil).expect(t, "delete again", http.StatusSeeOther)
		anna.post("/settings/lists/"+list+"/delete", nil).expect(t, "delete last list", http.StatusConflict)

		// Categories page.
		p = anna.get("/settings/categories").expect(t, "categories", http.StatusOK)
		cats := categoryRe.FindAllStringSubmatch(p.body, -1)
		if len(cats) != 10 { // nine defaults and Asian
			t.Fatalf("%d categories", len(cats))
		}
		anna.post("/settings/categories", url.Values{"name": {"produce"}, "color": {"herb"}}).
			expect(t, "duplicate category", http.StatusUnprocessableEntity)
		anna.post("/settings/categories", url.Values{"name": {"Snacks"}, "color": {"sky"}}).expect(t, "create category", http.StatusSeeOther)
		last := cats[len(cats)-1][1]
		anna.post("/settings/categories/"+last+"/move", url.Values{"to": {"0"}}).expect(t, "move category", http.StatusSeeOther)
		res := anna.datastar(http.MethodPost, "/settings/categories/"+last+"/move", nil)
		res.expect(t, "drop without index", http.StatusBadRequest)
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, anna.url("/settings/categories/"+last+"/move"), strings.NewReader(`{"to":2}`))
		req.Header.Set("Datastar-Request", "true")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		if p := anna.do(req); p.status != http.StatusOK || !strings.Contains(p.body, `id="categories"`) {
			t.Errorf("drop: %d\n%.500s", p.status, p.body)
		}
		anna.post("/settings/categories/"+last, url.Values{"name": {"Asia"}, "color": {"coral"}}).expect(t, "rename category", http.StatusSeeOther)
		anna.post("/settings/categories/"+last+"/delete", nil).expect(t, "delete category", http.StatusSeeOther, "/settings/categories?done=categoryDeleted")
		if p := anna.get(back); !strings.Contains(p.body, ">Uncategorised</h2>") {
			t.Error("item not uncategorised after its category was deleted")
		}

		// Writes are never GET.
		anna.get("/groceries/entries/"+id+"/check").expect(t, "GET check", http.StatusNotFound)
	})
}

func TestSuggestOverHTTP(t *testing.T) {
	_, srv := startServer(t, storetest.SQLite(t))
	anna, _ := household(t, srv)
	list := liveListRe.FindStringSubmatch(anna.get("/groceries").body)[1]
	for _, text := range []string{"tomatoes", "tomato paste"} {
		anna.post("/groceries/lists/"+list+"/entries", url.Values{"text": {text}}).expect(t, "add", http.StatusSeeOther)
	}

	res := anna.datastar(http.MethodGet, "/groceries/lists/"+list+"/suggest?datastar="+url.QueryEscape(`{"add":"2 tom"}`), nil)
	res.expect(t, "suggest", http.StatusOK)
	for _, want := range []string{"Amount: 2", "Tomatoes", "Tomato paste", "Already on the list", `Create new item “Tom”`, `"_sel":0`, `"_for":"2 tom"`} {
		if !strings.Contains(res.body, want) {
			t.Errorf("suggestions lack %q:\n%s", want, res.body)
		}
	}

	// Enter with the highlighted suggestion adds that item, with the
	// typed amount.
	m := regexp.MustCompile(`"_opts":\["([0-9a-f-]{36})"`).FindStringSubmatch(res.body)
	if m == nil {
		t.Fatalf("no options:\n%s", res.body)
	}
	anna.post("/groceries/lists/"+list+"/entries", url.Values{"text": {"2 tom"}, "sel": {m[1]}, "for": {"2 tom"}}).
		expect(t, "add highlighted", http.StatusSeeOther)
	// A stale highlight (computed for other text) is ignored: "Tom" is
	// created instead.
	anna.post("/groceries/lists/"+list+"/entries", url.Values{"text": {"tom"}, "sel": {m[1]}, "for": {"2 tom"}}).
		expect(t, "add stale", http.StatusSeeOther)
	p := anna.get("/groceries")
	if !strings.Contains(p.body, ">Tom</span>") || strings.Count(p.body, "Check off: Tomato paste, 2") != 1 {
		t.Errorf("unexpected list: %q", regexp.MustCompile(`Check off: [^"]*`).FindAllString(p.body, -1))
	}

	anna.get("/groceries/lists/"+list+"/suggest").expect(t, "suggest without Datastar", http.StatusBadRequest)
}
