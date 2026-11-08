package i18n

import (
	"context"
	"go/constant"
	"go/types"
	"path/filepath"
	"reflect"
	"testing"

	"golang.org/x/text/language"
	"golang.org/x/text/message"
	"golang.org/x/tools/go/packages"
)

func TestT(t *testing.T) {
	en := WithLocale(context.Background(), language.English)
	de := WithLocale(context.Background(), language.German)
	if got := T(en, "nav.groceries"); got != "Groceries" {
		t.Errorf("en = %q", got)
	}
	if got := T(de, "nav.groceries"); got != "Einkauf" {
		t.Errorf("de = %q", got)
	}
	if got := T(context.Background(), "nav.recipes"); got != "Recipes" {
		t.Errorf("no locale = %q, want English default", got)
	}
}

func TestMatch(t *testing.T) {
	cases := []struct {
		prefs []string
		want  language.Tag
	}{
		{nil, language.English},
		{[]string{"de-AT,de;q=0.9,en;q=0.8"}, language.German},
		{[]string{"fr-FR,fr;q=0.9"}, language.English},
		{[]string{"en", "de"}, language.English},
		{[]string{"", "de-CH"}, language.German},
	}
	for _, c := range cases {
		if got := Match(c.prefs...); got != c.want {
			t.Errorf("Match(%q) = %v, want %v", c.prefs, got, c.want)
		}
	}
}

// TestCatalogComplete fails when a translation has keys English lacks.
func TestCatalogComplete(t *testing.T) {
	en := messages[language.English]
	for tag, m := range messages {
		for k := range m {
			if _, ok := en[k]; !ok {
				t.Errorf("%s has key %q missing from en", tag, k)
			}
		}
	}
}

// TestKeysUsedExist type-checks the module and fails when any constant of
// type Key (a literal passed to T or to any Key parameter, or a declared Key
// constant) is missing from the English catalog. templ components are
// checked through their generated Go code.
func TestKeysUsedExist(t *testing.T) {
	if testing.Short() {
		t.Skip("type-checks the whole module")
	}
	en := messages[language.English]
	keyPkg := reflect.TypeFor[Key]().PkgPath()

	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo,
		Dir: filepath.Join("..", ".."),
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		t.Fatal(err)
	}
	if packages.PrintErrors(pkgs) > 0 {
		t.Fatal("packages failed to load")
	}

	checked := 0
	for _, pkg := range pkgs {
		for expr, tv := range pkg.TypesInfo.Types {
			if tv.Value == nil || tv.Value.Kind() != constant.String || !isKey(tv.Type, keyPkg) {
				continue
			}
			checked++
			if key := constant.StringVal(tv.Value); en[key] == "" {
				t.Errorf("%s: key %q missing from en", pkg.Fset.Position(expr.Pos()), key)
			}
		}
	}
	if checked == 0 {
		t.Fatal("found no keys; is the type check working?")
	}
}

func isKey(typ types.Type, pkgPath string) bool {
	named, ok := typ.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj.Name() == "Key" && obj.Pkg() != nil && obj.Pkg().Path() == pkgPath
}

func TestFallbackPerKey(t *testing.T) {
	b, err := build([]language.Tag{language.English, language.German}, map[language.Tag]map[string]string{
		language.English: {"greeting": "Hello", "farewell": "Goodbye"},
		language.German:  {"greeting": "Hallo"},
	})
	if err != nil {
		t.Fatal(err)
	}
	p := message.NewPrinter(language.German, message.Catalog(b))
	if got := p.Sprintf("greeting"); got != "Hallo" {
		t.Errorf("translated key = %q, want Hallo", got)
	}
	if got := p.Sprintf("farewell"); got != "Goodbye" {
		t.Errorf("untranslated key = %q, want English fallback Goodbye", got)
	}
}
