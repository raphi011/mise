package ai

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/text/language"

	"github.com/raphi011/mise/internal/live"
	"github.com/raphi011/mise/internal/store"
)

// Workers is how many imports run at once per process.
const Workers = 2

// pollInterval is how often idle workers look for queued jobs without a
// wake-up; StartImport wakes them right away.
const pollInterval = 10 * time.Second

// UserPublisher delivers events to one user's open pages.
type UserPublisher interface {
	PublishUser(userID uuid.UUID, e live.Event)
}

func liveImportChanged(user, id uuid.UUID) live.Event {
	return live.Event{Kind: live.ImportChanged, UserID: user, JobID: id}
}

// runner is the job runner's state, shared by the workers and the
// request handlers that cancel jobs.
type runner struct {
	mu sync.Mutex
	// running maps a running job to its cancel function.
	running map[uuid.UUID]*runningJob
	// pending holds cancel requests for jobs a worker claimed but has not
	// registered yet.
	pending map[uuid.UUID]bool
	wake    chan struct{}
}

type runningJob struct {
	cancel   context.CancelFunc
	canceled bool
}

func newRunner() *runner {
	return &runner{running: map[uuid.UUID]*runningJob{}, pending: map[uuid.UUID]bool{}, wake: make(chan struct{}, Workers)}
}

func (s *Service) wakeRunner() {
	for range Workers {
		select {
		case s.run.wake <- struct{}{}:
		default:
		}
	}
}

// cancelRunning cancels a running job and reports whether it was running.
func (s *Service) cancelRunning(id uuid.UUID) bool {
	s.run.mu.Lock()
	defer s.run.mu.Unlock()
	j, ok := s.run.running[id]
	if ok {
		j.canceled = true
		j.cancel()
	}
	return ok
}

// requestCancel cancels a job that is running or about to.
func (s *Service) requestCancel(id uuid.UUID) {
	s.run.mu.Lock()
	defer s.run.mu.Unlock()
	if j, ok := s.run.running[id]; ok {
		j.canceled = true
		j.cancel()
		return
	}
	s.run.pending[id] = true
}

// register records a running job's cancel function; it cancels right
// away when a cancel request came first.
func (s *Service) register(id uuid.UUID, cancel context.CancelFunc) *runningJob {
	s.run.mu.Lock()
	defer s.run.mu.Unlock()
	j := &runningJob{cancel: cancel}
	s.run.running[id] = j
	if s.run.pending[id] {
		delete(s.run.pending, id)
		j.canceled = true
		cancel()
	}
	return j
}

// unregister forgets a job and reports whether the user canceled it.
func (s *Service) unregister(id uuid.UUID) bool {
	s.run.mu.Lock()
	defer s.run.mu.Unlock()
	j := s.run.running[id]
	delete(s.run.running, id)
	delete(s.run.pending, id)
	return j != nil && j.canceled
}

// Run marks jobs a previous process left queued or running as failed
// (jobs created since New are this process's own), then runs queued jobs with Workers workers until ctx ends. Jobs still
// running then are marked failed as interrupted.
func (s *Service) Run(ctx context.Context) error {
	if s.db == nil {
		return errors.New("ai: Run needs Config.DB")
	}
	if err := s.failLeftovers(ctx); err != nil {
		return err
	}
	var wg sync.WaitGroup
	for range Workers {
		wg.Go(func() { s.work(ctx) })
	}
	wg.Wait()
	return nil
}

func (s *Service) failLeftovers(ctx context.Context) error {
	now := store.T(s.now())
	return s.db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
		res, err := q.Exec(ctx, `UPDATE import_jobs SET status = 'failed', error_code = ?, finished_at = ?, updated_at = ?
			WHERE status IN ('queued', 'running') AND created_at < ?`, string(CodeInterrupted), now, now, store.T(s.started))
		if err != nil {
			return fmt.Errorf("ai: fail leftover imports: %w", err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			s.log.InfoContext(ctx, "marked interrupted imports failed", slog.Int64("jobs", n))
		}
		return nil
	})
}

func (s *Service) work(ctx context.Context) {
	for ctx.Err() == nil {
		c, ok, err := s.claim(ctx)
		if err != nil && ctx.Err() == nil {
			s.log.ErrorContext(ctx, "claim import job", slog.Any("err", err))
		}
		if ok {
			s.runJob(ctx, c)
			continue
		}
		select {
		case <-ctx.Done():
		case <-s.run.wake:
		case <-time.After(pollInterval):
		}
	}
}

type claimed struct {
	ID          uuid.UUID
	HouseholdID uuid.UUID
	UserID      uuid.UUID
}

// claim takes the oldest queued job.
func (s *Service) claim(ctx context.Context) (claimed, bool, error) {
	var c claimed
	found := false
	err := s.db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
		for {
			err := q.Get(ctx, &c, `SELECT id, household_id, user_id FROM import_jobs
				WHERE status = 'queued' ORDER BY created_at, id LIMIT 1`)
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			now := store.T(s.now())
			res, err := q.Exec(ctx, `UPDATE import_jobs SET status = 'running', started_at = ?, updated_at = ?
				WHERE household_id = ? AND id = ? AND status = 'queued'`, now, now, c.HouseholdID, c.ID)
			if err != nil {
				return err
			}
			if n, err := res.RowsAffected(); err != nil || n == 1 {
				found = err == nil
				return err
			}
		}
	})
	return c, found, err
}

// runJob runs one claimed job to its end and records the outcome.
func (s *Service) runJob(base context.Context, c claimed) {
	ctx, cancel := context.WithTimeout(base, s.jobTimeout)
	defer cancel()
	s.register(c.ID, cancel)
	sc := s.db.ScopeFor(c.HouseholdID)
	s.publishJob(c.UserID, c.ID)

	var d Draft
	job, err := s.Job(ctx, sc, c.UserID, c.ID)
	if err == nil {
		p := &jobProgress{s: s, sc: sc, job: c}
		d, err = s.importJob(ctx, sc, job, s.userLanguage(ctx, c), p)
	}
	canceled := s.unregister(c.ID)

	// The outcome is written even when ctx has ended.
	wctx, wcancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer wcancel()
	switch {
	case canceled:
		err = s.deleteJob(wctx, sc, c.ID)
	case base.Err() != nil:
		err = s.finishJob(wctx, sc, c.ID, JobFailed, nil, CodeInterrupted, "")
	case err == nil:
		err = s.finishJob(wctx, sc, c.ID, JobReady, &d, "", "")
	default:
		code, detail := errorCode(err)
		if code == CodeInterrupted {
			s.log.ErrorContext(wctx, "import failed", slog.String("job", c.ID.String()), slog.Any("err", err))
		}
		err = s.finishJob(wctx, sc, c.ID, JobFailed, nil, code, detail)
	}
	if err != nil {
		s.log.ErrorContext(wctx, "record import outcome", slog.String("job", c.ID.String()), slog.Any("err", err))
	}
	s.publishJob(c.UserID, c.ID)
}

// importJob builds a job's draft: from the page's schema.org data without
// a model, else with the tool loop when AI import is on.
func (s *Service) importJob(ctx context.Context, sc store.Scoped, job Job, lang language.Tag, p progress) (Draft, error) {
	if job.SourceText != "" {
		i := p.start(JobStep{Kind: StepReadText})
		m, perms, err := s.model(ctx, sc)
		if err != nil {
			p.finish(i, StepFailed)
			return Draft{}, err
		}
		p.finish(i, StepDone)
		return s.runLoop(ctx, sc, m, loopInput{Text: job.SourceText, Lang: lang, Perms: perms}, p)
	}

	host := job.SourceURL
	if u, err := ParseURL(job.SourceURL); err == nil {
		host = u.Hostname()
	}
	i := p.start(JobStep{Kind: StepReadPage, Text: host, Tool: stepTool("fetch_page", job.SourceURL)})
	res, err := s.fetcher.Fetch(ctx, job.SourceURL, KindPage)
	if err != nil {
		p.finish(i, StepFailed)
		return Draft{}, err
	}
	page := ParsePage(res)
	p.finish(i, StepDone)
	if page.Recipe != nil {
		d, err := s.draftFromPage(ctx, sc, page, lang)
		if err != nil {
			return Draft{}, err
		}
		matched := 0
		for _, ing := range d.Ingredients {
			if ing.Match != MatchNew {
				matched++
			}
		}
		p.finish(p.start(JobStep{Kind: StepMatch, Text: strconv.Itoa(matched) + "/" + strconv.Itoa(len(d.Ingredients))}), StepDone)
		return d, nil
	}
	if strings.TrimSpace(page.Text) == "" {
		return Draft{}, ErrNoRecipe
	}
	m, perms, err := s.model(ctx, sc)
	if errors.Is(err, ErrDisabled) {
		return Draft{}, ErrNoRecipe
	}
	if err != nil {
		return Draft{}, err
	}
	return s.runLoop(ctx, sc, m, loopInput{SourceURL: res.URL, Title: page.Title, Text: page.Text,
		Lang: amountLang(page.Lang, lang), Perms: perms}, p)
}

// model returns the household's model client and tool switches.
func (s *Service) model(ctx context.Context, sc store.Scoped) (Model, Permissions, error) {
	c, err := s.Connection(ctx, sc)
	if err != nil {
		return nil, Permissions{}, err
	}
	st, err := s.Settings(ctx, sc)
	if err != nil {
		return nil, Permissions{}, err
	}
	m, err := s.newProvider(c)
	if err != nil {
		return nil, Permissions{}, err
	}
	return m, st.Permissions, nil
}

// userLanguage is the importer's language: their own, else the
// household's default.
func (s *Service) userLanguage(ctx context.Context, c claimed) language.Tag {
	var row struct {
		Language        *string
		DefaultLanguage string
	}
	err := s.db.System().Read().Get(ctx, &row, `SELECT u.language, h.default_language FROM users u, households h
		WHERE u.id = ? AND h.id = ?`, c.UserID, c.HouseholdID)
	if err != nil {
		return language.English
	}
	for _, l := range []*string{row.Language, &row.DefaultLanguage} {
		if l != nil && *l != "" {
			if tag, err := language.Parse(*l); err == nil {
				return tag
			}
		}
	}
	return language.English
}

// errorCode classifies why an import failed. detail is the provider's
// message, shown under "See details".
func errorCode(err error) (ErrorCode, string) {
	var pe *ProviderError
	var se *StatusError
	switch {
	case errors.Is(err, ErrNoRecipe):
		return CodeNoRecipe, ""
	case errors.Is(err, ErrBlockedAddress):
		return CodeBlockedAddress, ""
	case errors.Is(err, ErrTooLarge):
		return CodeTooLarge, ""
	case errors.Is(err, ErrBadURL), errors.Is(err, ErrContentType), errors.Is(err, ErrTooManyRedirects), errors.Is(err, ErrOtherHost), errors.As(err, &se),
		errors.Is(err, errFetch):
		return CodeFetchFailed, ""
	case errors.As(err, &pe):
		return CodeProviderError, truncate(pe.Message, 1000)
	case errors.Is(err, ErrDisabled), errors.Is(err, ErrUnreadable), errors.Is(err, errNoProvider):
		return CodeProviderError, ""
	case errors.Is(err, ErrRefused):
		return CodeRefused, ""
	case errors.Is(err, ErrLimit), errors.Is(err, context.DeadlineExceeded):
		return CodeLimit, ""
	}
	return CodeInterrupted, ""
}

func (s *Service) finishJob(ctx context.Context, sc store.Scoped, id uuid.UUID, status JobStatus, d *Draft, code ErrorCode, detail string) error {
	var draft, codeArg, detailArg *string
	if d != nil {
		b, err := json.Marshal(d)
		if err != nil {
			return err
		}
		v := string(b)
		draft = &v
	}
	if code != "" {
		v := string(code)
		codeArg = &v
	}
	if detail != "" {
		detailArg = &detail
	}
	now := store.T(s.now())
	return sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		_, err := q.Exec(ctx, `UPDATE import_jobs SET status = ?, draft = ?, error_code = ?, error_detail = ?, finished_at = ?, updated_at = ?
			WHERE household_id = ? AND id = ?`, string(status), draft, codeArg, detailArg, now, now, sc.HouseholdID(), id)
		return err
	})
}

func (s *Service) deleteJob(ctx context.Context, sc store.Scoped, id uuid.UUID) error {
	return sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		_, err := q.Exec(ctx, `DELETE FROM import_jobs WHERE household_id = ? AND id = ?`, sc.HouseholdID(), id)
		return err
	})
}

// jobProgress stores a running job's steps and tells the user's pages.
type jobProgress struct {
	s     *Service
	sc    store.Scoped
	job   claimed
	steps []JobStep
}

func (p *jobProgress) start(st JobStep) int {
	st.State = StepRunning
	p.steps = append(p.steps, st)
	p.save()
	return len(p.steps) - 1
}

func (p *jobProgress) finish(i int, state StepState) {
	p.steps[i].State = state
	p.save()
}

func (p *jobProgress) save() {
	b, err := json.Marshal(p.steps)
	if err != nil {
		return
	}
	// Steps are written even after the job's context ended, so the list
	// shows how far it got.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	now := store.T(p.s.now())
	err = p.sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		_, err := q.Exec(ctx, `UPDATE import_jobs SET steps = ?, updated_at = ? WHERE household_id = ? AND id = ?`,
			string(b), now, p.sc.HouseholdID(), p.job.ID)
		return err
	})
	if err != nil {
		p.s.log.Error("save import steps", slog.String("job", p.job.ID.String()), slog.Any("err", err))
		return
	}
	p.s.publishJob(p.job.UserID, p.job.ID)
}
