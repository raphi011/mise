// Package i18n holds the UI message catalog and the request locale.
//
// Messages live in locales/<lang>.json as flat key → format string maps
// (fmt verbs, e.g. "%d to get"). English is the source of truth; other
// languages may lag and fall back to English per key.
//
// Keys are of type Key. Write them as string literals where they are used
// (i18n.T(ctx, "nav.recipes"), or passed to a Key parameter) so that the
// catalog test can check every key in the code base statically.
package i18n

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"golang.org/x/text/language"
	"golang.org/x/text/message"
	"golang.org/x/text/message/catalog"
)

//go:embed locales/*.json
var localesFS embed.FS

// Supported lists the UI languages, default first.
var Supported = []language.Tag{language.English, language.German}

var (
	cat      *catalog.Builder
	messages map[language.Tag]map[string]string
	matcher  = language.NewMatcher(Supported)
)

func init() {
	var err error
	cat, messages, err = load()
	if err != nil {
		panic(err)
	}
}

// load reads the embedded locale files and builds the catalog.
func load() (*catalog.Builder, map[language.Tag]map[string]string, error) {
	all := map[language.Tag]map[string]string{}
	for _, tag := range Supported {
		name := path.Join("locales", tag.String()+".json")
		data, err := localesFS.ReadFile(name)
		if err != nil {
			return nil, nil, fmt.Errorf("i18n: %w", err)
		}
		var m map[string]string
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, nil, fmt.Errorf("i18n: %s: %w", name, err)
		}
		all[tag] = m
	}
	b, err := build(Supported, all)
	return b, all, err
}

// build returns a catalog of msgs, a map of language to key to message, for
// the languages in tags (default first). catalog.Fallback only applies when a
// language has no messages at all, so every other language is filled with
// the default language's message for each key it lacks.
func build(tags []language.Tag, msgs map[language.Tag]map[string]string) (*catalog.Builder, error) {
	def := msgs[tags[0]]
	b := catalog.NewBuilder(catalog.Fallback(tags[0]))
	for _, tag := range tags {
		m := msgs[tag]
		for k, v := range def {
			if tv, ok := m[k]; ok {
				v = tv
			}
			if err := b.SetString(tag, k, v); err != nil {
				return nil, fmt.Errorf("i18n: %s: %s: %w", tag, k, err)
			}
		}
	}
	return b, nil
}

type ctxKey struct{}

type locale struct {
	tag     language.Tag
	printer *message.Printer
}

// WithLocale returns a context whose messages are rendered in tag.
func WithLocale(ctx context.Context, tag language.Tag) context.Context {
	return context.WithValue(ctx, ctxKey{}, locale{tag: tag, printer: message.NewPrinter(tag, message.Catalog(cat))})
}

func fromContext(ctx context.Context) locale {
	if l, ok := ctx.Value(ctxKey{}).(locale); ok {
		return l
	}
	return locale{tag: Supported[0], printer: message.NewPrinter(Supported[0], message.Catalog(cat))}
}

// Locale returns the context's language.
func Locale(ctx context.Context) language.Tag { return fromContext(ctx).tag }

// Key is a message catalog key.
type Key string

// T renders the message key in the context's language, falling back to
// English. Unknown keys render as the key itself, which the catalog test
// catches.
func T(ctx context.Context, key Key, args ...any) string {
	return fromContext(ctx).printer.Sprintf(string(key), args...)
}

// Match picks the best supported language for the given preferences, each a
// language tag or an Accept-Language header value. Earlier preferences win;
// empty ones are ignored.
func Match(prefs ...string) language.Tag {
	var tags []language.Tag
	for _, p := range prefs {
		if strings.TrimSpace(p) == "" {
			continue
		}
		t, _, err := language.ParseAcceptLanguage(p)
		if err == nil {
			tags = append(tags, t...)
		}
	}
	_, i, _ := matcher.Match(tags...)
	return Supported[i]
}
