package ai

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/store"
)

// JobStatus is an import job's state.
type JobStatus string

// Job statuses.
const (
	JobQueued   JobStatus = "queued"
	JobRunning  JobStatus = "running"
	JobReady    JobStatus = "ready"
	JobFailed   JobStatus = "failed"
	JobCanceled JobStatus = "canceled"
)

// ErrorCode says why an import failed; the pages word it.
type ErrorCode string

// Error codes.
const (
	CodeNoRecipe       ErrorCode = "no_recipe"
	CodeFetchFailed    ErrorCode = "fetch_failed"
	CodeBlockedAddress ErrorCode = "blocked_address"
	CodeTooLarge       ErrorCode = "too_large"
	CodeProviderError  ErrorCode = "provider_error"
	CodeRefused        ErrorCode = "refused"
	CodeLimit          ErrorCode = "limit"
	CodeInterrupted    ErrorCode = "interrupted"
)

// MaxActiveJobs is how many queued or running imports one user may have.
const MaxActiveJobs = 3

// ReadyRetention is how long ready and failed imports are kept after they
// finish.
const ReadyRetention = 14 * 24 * time.Hour

// Import errors.
var (
	// ErrTooManyJobs: the user already has MaxActiveJobs imports going.
	ErrTooManyJobs = errors.New("ai: too many imports at once")
	// ErrJobNotFound: no such job of this user.
	ErrJobNotFound = errors.New("ai: import not found")
)

// Job is an import job as its pages show it.
type Job struct {
	ID         uuid.UUID
	UserID     uuid.UUID
	SourceURL  string
	SourceText string
	Status     JobStatus
	Steps      []JobStep
	Draft      *Draft
	ErrorCode  ErrorCode
	// ErrorDetail is the provider's message for provider_error.
	ErrorDetail string
	CreatedAt   time.Time
	StartedAt   *time.Time
	FinishedAt  *time.Time
}

// Active reports whether the job is queued or running.
func (j Job) Active() bool { return j.Status == JobQueued || j.Status == JobRunning }

// ImportInput is a submitted import form: a link or pasted text.
type ImportInput struct {
	URL  string
	Text string
}

// StartImport queues an import for user. Pasted text needs AI import to be
// on (ErrDisabled); a link always works, since pages with recipe data
// import without a model.
func (s *Service) StartImport(ctx context.Context, sc store.Scoped, user uuid.UUID, in ImportInput) (uuid.UUID, error) {
	var srcURL, srcText *string
	switch u, t := strings.TrimSpace(in.URL), strings.TrimSpace(in.Text); {
	case t != "":
		if len(t) > MaxTextBytes {
			return uuid.Nil, FieldErrors{"text": "import.error.textLong"}
		}
		if !utf8.ValidString(t) {
			return uuid.Nil, FieldErrors{"text": "import.error.text"}
		}
		if _, err := s.Connection(ctx, sc); err != nil {
			return uuid.Nil, err
		}
		srcText = &t
	case u != "":
		if !strings.Contains(u, "://") {
			u = "https://" + u
		}
		p, err := url.Parse(u)
		if err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" || p.User != nil || len(u) > maxDraftURL {
			return uuid.Nil, FieldErrors{"url": "import.error.url"}
		}
		u = p.String()
		srcURL = &u
	default:
		return uuid.Nil, FieldErrors{"url": "import.error.empty"}
	}
	id := store.NewID()
	now := store.T(s.now())
	err := sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		var active int
		if err := q.Get(ctx, &active, `SELECT count(*) FROM import_jobs
			WHERE household_id = ? AND user_id = ? AND status IN ('queued', 'running')`, sc.HouseholdID(), user); err != nil {
			return err
		}
		if active >= MaxActiveJobs {
			return ErrTooManyJobs
		}
		_, err := q.Exec(ctx, `INSERT INTO import_jobs (id, household_id, user_id, source_url, source_text, status, steps,
				created_at, updated_at) VALUES (?, ?, ?, ?, ?, 'queued', '[]', ?, ?)`,
			id, sc.HouseholdID(), user, srcURL, srcText, now, now)
		return err
	})
	if err != nil {
		return uuid.Nil, err
	}
	s.wakeRunner()
	s.publishJob(user, id)
	return id, nil
}

// Job returns one of user's jobs.
func (s *Service) Job(ctx context.Context, sc store.Scoped, user, id uuid.UUID) (Job, error) {
	var row jobRow
	err := sc.Read().Get(ctx, &row, jobSelect+` AND user_id = ? AND id = ?`, sc.HouseholdID(), user, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrJobNotFound
	}
	if err != nil {
		return Job{}, fmt.Errorf("ai: job: %w", err)
	}
	return row.job()
}

// Jobs returns user's jobs, newest first.
func (s *Service) Jobs(ctx context.Context, sc store.Scoped, user uuid.UUID) ([]Job, error) {
	var rows []jobRow
	if err := sc.Read().Select(ctx, &rows, jobSelect+` AND user_id = ? ORDER BY created_at DESC, id`, sc.HouseholdID(), user); err != nil {
		return nil, fmt.Errorf("ai: jobs: %w", err)
	}
	out := make([]Job, 0, len(rows))
	for _, r := range rows {
		j, err := r.job()
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, nil
}

// CancelImport stops one of user's imports: a running job's context is
// canceled and the runner deletes it; any other job is deleted now.
// Discarding a draft is the same.
func (s *Service) CancelImport(ctx context.Context, sc store.Scoped, user, id uuid.UUID) error {
	if s.cancelRunning(id) {
		return nil
	}
	var deleted int64
	err := sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		res, err := q.Exec(ctx, `DELETE FROM import_jobs WHERE household_id = ? AND user_id = ? AND id = ? AND status <> 'running'`,
			sc.HouseholdID(), user, id)
		if err != nil {
			return fmt.Errorf("ai: cancel import: %w", err)
		}
		deleted, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return err
	}
	if deleted == 0 {
		// Gone, or claimed by a worker that has not registered its cancel
		// yet; the worker checks for a cancel request when it does.
		if _, err := s.Job(ctx, sc, user, id); err != nil {
			return err
		}
		s.requestCancel(id)
		return nil
	}
	s.publishJob(user, id)
	return nil
}

func (s *Service) publishJob(user, id uuid.UUID) {
	if s.pub != nil {
		s.pub.PublishUser(user, liveImportChanged(user, id))
	}
}

const jobSelect = `SELECT id, user_id, source_url, source_text, status, steps, draft, error_code, error_detail,
	created_at, started_at, finished_at FROM import_jobs WHERE household_id = ?`

type jobRow struct {
	ID          uuid.UUID
	UserID      uuid.UUID
	SourceURL   *string
	SourceText  *string
	Status      JobStatus
	Steps       string
	Draft       *string
	ErrorCode   *string
	ErrorDetail *string
	CreatedAt   store.Time
	StartedAt   *store.Time
	FinishedAt  *store.Time
}

func (r jobRow) job() (Job, error) {
	j := Job{ID: r.ID, UserID: r.UserID, Status: r.Status, CreatedAt: r.CreatedAt.Time}
	if r.SourceURL != nil {
		j.SourceURL = *r.SourceURL
	}
	if r.SourceText != nil {
		j.SourceText = *r.SourceText
	}
	if err := json.Unmarshal([]byte(r.Steps), &j.Steps); err != nil {
		return Job{}, fmt.Errorf("ai: job %s steps: %w", r.ID, err)
	}
	if r.Draft != nil {
		j.Draft = &Draft{}
		if err := json.Unmarshal([]byte(*r.Draft), j.Draft); err != nil {
			return Job{}, fmt.Errorf("ai: job %s draft: %w", r.ID, err)
		}
	}
	if r.ErrorCode != nil {
		j.ErrorCode = ErrorCode(*r.ErrorCode)
	}
	if r.ErrorDetail != nil {
		j.ErrorDetail = *r.ErrorDetail
	}
	if r.StartedAt != nil {
		t := r.StartedAt.Time
		j.StartedAt = &t
	}
	if r.FinishedAt != nil {
		t := r.FinishedAt.Time
		j.FinishedAt = &t
	}
	return j, nil
}
