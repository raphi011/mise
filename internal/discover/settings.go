package discover

import (
	"context"
	"fmt"
	"slices"

	"golang.org/x/text/language"

	"github.com/raphi011/mise/internal/store"
)

// SiteSetting is a site as a household's settings show it.
type SiteSetting struct {
	Site Site
	// Enabled reports whether the household searches the site.
	Enabled bool
}

// Settings lists every site with whether the household searches it, in
// site order. A site is searched by default when it is in the household's
// default language; an admin's choice otherwise is kept per site.
func (s *Service) Settings(ctx context.Context, sc store.Scoped) ([]SiteSetting, error) {
	q := sc.Read()
	var lang string
	if err := q.Get(ctx, &lang, `SELECT default_language FROM households WHERE id = ?`, sc.HouseholdID()); err != nil {
		return nil, fmt.Errorf("discover: household language: %w", err)
	}
	var rows []struct {
		Site    string
		Enabled bool
	}
	if err := q.Select(ctx, &rows, `SELECT site, enabled FROM household_recipe_sites WHERE household_id = ?`, sc.HouseholdID()); err != nil {
		return nil, fmt.Errorf("discover: site settings: %w", err)
	}
	chosen := map[string]bool{}
	for _, r := range rows {
		chosen[r.Site] = r.Enabled
	}
	out := make([]SiteSetting, len(s.sites))
	for i, st := range s.sites {
		on, ok := chosen[st.Name()]
		if !ok {
			on = byDefault(st, lang)
		}
		out[i] = SiteSetting{Site: st, Enabled: on}
	}
	return out, nil
}

// Enabled names the sites the household searches, in site order.
func (s *Service) Enabled(ctx context.Context, sc store.Scoped) ([]string, error) {
	settings, err := s.Settings(ctx, sc)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, st := range settings {
		if st.Enabled {
			names = append(names, st.Site.Name())
		}
	}
	return names, nil
}

// SaveSettings makes the household search the sites named in on and no
// others. A choice that matches the default is not kept, so the site keeps
// following the household's language.
func (s *Service) SaveSettings(ctx context.Context, sc store.Scoped, on []string) error {
	now := store.T(s.now())
	return sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		var lang string
		if err := q.Get(ctx, &lang, `SELECT default_language FROM households WHERE id = ?`, sc.HouseholdID()); err != nil {
			return fmt.Errorf("discover: household language: %w", err)
		}
		for _, st := range s.sites {
			enabled := slices.Contains(on, st.Name())
			if enabled == byDefault(st, lang) {
				if _, err := q.Exec(ctx, `DELETE FROM household_recipe_sites WHERE household_id = ? AND site = ?`,
					sc.HouseholdID(), st.Name()); err != nil {
					return fmt.Errorf("discover: save site settings: %w", err)
				}
				continue
			}
			if _, err := q.Exec(ctx, `
				INSERT INTO household_recipe_sites (household_id, site, enabled, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?)
				ON CONFLICT (household_id, site) DO UPDATE SET enabled = excluded.enabled, updated_at = excluded.updated_at`,
				sc.HouseholdID(), st.Name(), enabled, now, now); err != nil {
				return fmt.Errorf("discover: save site settings: %w", err)
			}
		}
		return nil
	})
}

// byDefault reports whether site is searched when no admin chose: when
// it is in the household's language.
func byDefault(site Site, householdLang string) bool {
	tag, err := language.Parse(householdLang)
	if err != nil {
		return false
	}
	hb, _ := tag.Base()
	sb, _ := site.Language().Base()
	return hb == sb
}
