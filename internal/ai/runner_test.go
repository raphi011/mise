package ai_test

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/ai"
	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
)

func TestStartImport(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		e := newImportEnv(t, db, "home", nil)
		other := newImportEnv(t, db, "other", nil)
		anna, ben := e.hh.Admin, e.hh.Member

		for name, in := range map[string]ai.ImportInput{"empty": {}, "ftp": {URL: "ftp://x/y"}, "no host": {URL: "https://"}} {
			var fe ai.FieldErrors
			if _, err := e.s.StartImport(ctx, e.sc, anna, in); !errors.As(err, &fe) {
				t.Errorf("%s: %v", name, err)
			}
		}
		if _, err := e.s.StartImport(ctx, e.sc, anna, ai.ImportInput{Text: "Dal"}); !errors.Is(err, ai.ErrDisabled) {
			t.Errorf("text with AI off: %v", err)
		}
		var ids []uuid.UUID
		for _, u := range []string{"example.com/a", "https://example.com/b", "http://example.com/c"} {
			id, err := e.s.StartImport(ctx, e.sc, anna, ai.ImportInput{URL: u})
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
		}
		if _, err := e.s.StartImport(ctx, e.sc, anna, ai.ImportInput{URL: "example.com/d"}); !errors.Is(err, ai.ErrTooManyJobs) {
			t.Errorf("fourth import: %v", err)
		}
		if _, err := e.s.StartImport(ctx, e.sc, ben, ai.ImportInput{URL: "example.com/d"}); err != nil {
			t.Errorf("another member's import: %v", err)
		}
		j, err := e.s.Job(ctx, e.sc, anna, ids[0])
		if err != nil || j.SourceURL != "https://example.com/a" || j.Status != ai.JobQueued || len(j.Steps) != 0 || !j.Active() {
			t.Errorf("job %+v, %v", j, err)
		}
		if e.pub.count(ids[0]) != 1 {
			t.Errorf("start not published")
		}
		// Jobs are per user and per household.
		if _, err := e.s.Job(ctx, e.sc, ben, ids[0]); !errors.Is(err, ai.ErrJobNotFound) {
			t.Errorf("another member's job: %v", err)
		}
		if _, err := other.s.Job(ctx, other.sc, anna, ids[0]); !errors.Is(err, ai.ErrJobNotFound) {
			t.Errorf("another household's job: %v", err)
		}
		if err := other.s.CancelImport(ctx, other.sc, other.hh.Admin, ids[0]); !errors.Is(err, ai.ErrJobNotFound) {
			t.Errorf("cancel another household's job: %v", err)
		}
		jobs, err := e.s.Jobs(ctx, e.sc, anna)
		if err != nil || len(jobs) != 3 {
			t.Fatalf("jobs %+v, %v", jobs, err)
		}
		if l, _ := other.s.Jobs(ctx, other.sc, anna); len(l) != 0 {
			t.Errorf("other household lists %d jobs", len(l))
		}
		// Canceling a queued job deletes it.
		if err := e.s.CancelImport(ctx, e.sc, anna, ids[1]); err != nil {
			t.Fatal(err)
		}
		if _, err := e.s.Job(ctx, e.sc, anna, ids[1]); !errors.Is(err, ai.ErrJobNotFound) {
			t.Errorf("canceled job: %v", err)
		}
		if _, err := e.s.StartImport(ctx, e.sc, anna, ai.ImportInput{URL: "example.com/d"}); err != nil {
			t.Errorf("import after cancel: %v", err)
		}
	})
}

// blockingModel waits for its context to end.
type blockingModel struct{ started chan struct{} }

func (m *blockingModel) Test(context.Context) error { return nil }

func (m *blockingModel) Chat(ctx context.Context, _ ai.Request) (ai.Response, error) {
	select {
	case m.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return ai.Response{}, ctx.Err()
}

type failingModel struct{ err error }

func (m failingModel) Test(context.Context) error { return m.err }
func (m failingModel) Chat(context.Context, ai.Request) (ai.Response, error) {
	return ai.Response{}, m.err
}

// runImports runs e's job runner until the test ends.
func runImports(t *testing.T, e *importEnv) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() {
		if err := e.s.Run(ctx); err != nil {
			t.Error(err)
		}
	})
	t.Cleanup(func() {
		cancel()
		wg.Wait()
	})
}

// waitJob polls until the job leaves the queued and running states.
func waitJob(t *testing.T, e *importEnv, id uuid.UUID) ai.Job {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		j, err := e.s.Job(t.Context(), e.sc, e.hh.Admin, id)
		if err != nil {
			t.Fatalf("job %s: %v", id, err)
		}
		if !j.Active() {
			return j
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %s still active", id)
	return ai.Job{}
}

func stepKinds(j ai.Job) []string {
	var out []string
	for _, s := range j.Steps {
		out = append(out, string(s.Kind)+" "+string(s.State))
	}
	return out
}

func TestRunImports(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/dal":
				w.Header().Set("Content-Type", "text/html")
				_, _ = w.Write([]byte(dalPage))
			case "/blog":
				w.Header().Set("Content-Type", "text/html")
				_, _ = w.Write([]byte(blogPage))
			default:
				http.NotFound(w, r)
			}
		})
		e := newImportEnv(t, db, "home", fetcher(t, 0, srv))
		e.item(t, "Red lentils", "Onion")
		anna := e.hh.Admin

		// A job a previous process left running fails as interrupted.
		leftover := store.NewID()
		before := store.T(time.Now().Add(-time.Minute))
		err := db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
			_, err := q.Exec(ctx, `INSERT INTO import_jobs (id, household_id, user_id, source_url, status, steps, created_at, updated_at)
				VALUES (?, ?, ?, 'https://example.com/x', 'running', '[]', ?, ?)`, leftover, e.hh.ID, anna, before, before)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		runImports(t, e)
		if j := waitJob(t, e, leftover); j.Status != ai.JobFailed || j.ErrorCode != ai.CodeInterrupted {
			t.Errorf("leftover %+v", j)
		}

		start := func(in ai.ImportInput) uuid.UUID {
			t.Helper()
			id, err := e.s.StartImport(ctx, e.sc, anna, in)
			if err != nil {
				t.Fatal(err)
			}
			return id
		}

		// Schema.org data imports without a model.
		id := start(ai.ImportInput{URL: srv.URL + "/dal"})
		j := waitJob(t, e, id)
		if j.Status != ai.JobReady || j.Draft == nil || j.Draft.Name != "Red lentil dal & rice" || j.StartedAt == nil || j.FinishedAt == nil {
			t.Fatalf("dal job %+v", j)
		}
		if got := stepKinds(j); !slices.Equal(got, []string{"read_page done", "match done"}) || j.Steps[1].Text != "2/3" {
			t.Errorf("steps %v (%q)", got, j.Steps)
		}
		if e.pub.count(id) < 4 {
			t.Errorf("%d events for the job", e.pub.count(id))
		}

		// Without schema.org data and with AI off there is no recipe.
		if j := waitJob(t, e, start(ai.ImportInput{URL: srv.URL + "/blog"})); j.Status != ai.JobFailed || j.ErrorCode != ai.CodeNoRecipe {
			t.Errorf("blog without AI %+v", j)
		}
		for path, code := range map[string]ai.ErrorCode{"/missing": ai.CodeFetchFailed, "http://127.0.0.1:1/x": ai.CodeBlockedAddress} {
			u := path
			if strings.HasPrefix(path, "/") {
				u = srv.URL + path
			}
			if j := waitJob(t, e, start(ai.ImportInput{URL: u})); j.Status != ai.JobFailed || j.ErrorCode != code {
				t.Errorf("%s: %+v", path, j)
			}
		}

		// With AI on, the page text goes to the model.
		if err := e.s.SaveSettings(ctx, e.sc, anna, ai.SettingsInput{Enabled: true, Provider: ai.ProviderOllama, Model: "m",
			Permissions: ai.DefaultPermissions}); err != nil {
			t.Fatal(err)
		}
		e.setModel(script(t, calls("create_recipe", recipe("Grandma's dal", ingredient{"250 g red lentils", ""}))))
		j = waitJob(t, e, start(ai.ImportInput{URL: srv.URL + "/blog"}))
		if j.Status != ai.JobReady || j.Draft.Name != "Grandma's dal" || j.Draft.Ingredients[0].Match != ai.MatchExact ||
			j.Draft.SourceURL != srv.URL+"/blog" {
			t.Errorf("blog with AI %+v", j)
		}
		if got := stepKinds(j); !slices.Equal(got, []string{"read_page done", "find_recipe done", "create_recipe done"}) {
			t.Errorf("steps %v", got)
		}
		// Pasted text.
		e.setModel(script(t, calls("create_recipe", recipe("Linsen", ingredient{"250 g Linsen", ""}))))
		j = waitJob(t, e, start(ai.ImportInput{Text: "Linsen\n250 g Linsen"}))
		if j.Status != ai.JobReady || j.Draft.Name != "Linsen" || j.SourceText == "" {
			t.Errorf("pasted text %+v", j)
		}

		e.setModel(failingModel{&ai.ProviderError{Status: 401, Message: "invalid x-api-key"}})
		if j := waitJob(t, e, start(ai.ImportInput{Text: "x"})); j.ErrorCode != ai.CodeProviderError || j.ErrorDetail != "invalid x-api-key" {
			t.Errorf("provider error %+v", j)
		}
		e.setModel(script(t, says("", ai.StopRefusal)))
		if j := waitJob(t, e, start(ai.ImportInput{Text: "x"})); j.ErrorCode != ai.CodeRefused {
			t.Errorf("refusal %+v", j)
		}

		// Cancel stops a running job and deletes it.
		bm := &blockingModel{started: make(chan struct{}, 1)}
		e.setModel(bm)
		id = start(ai.ImportInput{Text: "x"})
		select {
		case <-bm.started:
		case <-time.After(10 * time.Second):
			t.Fatal("job did not start")
		}
		if err := e.s.CancelImport(ctx, e.sc, anna, id); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, err := e.s.Job(ctx, e.sc, anna, id); errors.Is(err, ai.ErrJobNotFound) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("canceled job still there")
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
}

func TestRunImportsTimeLimit(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		e := newImportEnv(t, db, "home", nil)
		e.s.SetJobTimeout(100 * time.Millisecond)
		e.setModel(&blockingModel{started: make(chan struct{}, 1)})
		if err := e.s.SaveSettings(ctx, e.sc, e.hh.Admin, ai.SettingsInput{Enabled: true, Provider: ai.ProviderOllama, Model: "m"}); err != nil {
			t.Fatal(err)
		}
		runImports(t, e)
		id, err := e.s.StartImport(ctx, e.sc, e.hh.Admin, ai.ImportInput{Text: "x"})
		if err != nil {
			t.Fatal(err)
		}
		// The model turn the limit cut short is marked failed.
		if j := waitJob(t, e, id); j.ErrorCode != ai.CodeLimit || len(j.Steps) != 2 || j.Steps[0].State != ai.StepDone ||
			j.Steps[1].Kind != ai.StepFindRecipe || j.Steps[1].State != ai.StepFailed {
			t.Errorf("timed out job %+v", j)
		}
	})
}
