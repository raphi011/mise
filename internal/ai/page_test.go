package ai_test

import (
	"net/url"
	"slices"
	"strings"
	"testing"

	"golang.org/x/text/language"

	"github.com/raphi011/mise/internal/ai"
)

func parse(t *testing.T, contentType, body string) ai.Page {
	t.Helper()
	u, _ := url.Parse("https://food.example/recipes/dal")
	return ai.ParsePage(ai.Fetched{URL: u, ContentType: contentType, Body: []byte(body)})
}

const dalPage = `<!doctype html><html lang="en-GB"><head><title>Red lentil dal | Food</title>
<meta property="og:image" content="/img/og.jpg">
<script type="application/ld+json">{"@context":"https://schema.org","@graph":[
 {"@type":"WebSite","name":"Food"},
 {"@type":["Recipe","NewsArticle"],"name":"Red lentil dal &amp; rice","description":"<p>A <b>quick</b> dal.</p>",
  "image":[{"@type":"ImageObject","url":"https://cdn.food.example/dal.jpg"}],
  "prepTime":"PT10M","totalTime":"PT1H","recipeYield":["4","4 servings"],
  "recipeIngredient":["250 g red lentils","1 onion, finely chopped","2 tbsp oil"],
  "recipeInstructions":[{"@type":"HowToSection","name":"Dal","itemListElement":[
    {"@type":"HowToStep","text":"Rinse the lentils."},
    {"@type":"HowToStep","name":"Fry the onion in the oil."}]},
   "Simmer for 20 minutes."],
  "keywords":"vegan, quick","recipeCategory":"Dinner","recipeCuisine":["Indian"]}]}</script>
<style>.x{}</style></head>
<body><nav>Home Recipes</nav><h1>Red lentil dal</h1><p>Serves  4.</p><footer>© Food</footer></body></html>`

func TestParsePageJSONLD(t *testing.T) {
	p := parse(t, "text/html; charset=utf-8", dalPage)
	if p.Title != "Red lentil dal | Food" || p.Image != "https://food.example/img/og.jpg" || p.Lang != language.MustParse("en-GB") {
		t.Errorf("page %+v", p)
	}
	r := p.Recipe
	if r == nil {
		t.Fatal("no recipe")
	}
	if r.Name != "Red lentil dal & rice" || r.Description != "A quick dal." || r.ImageURL != "https://cdn.food.example/dal.jpg" {
		t.Errorf("recipe %+v", r)
	}
	if r.PrepMin != 10 || r.CookMin != 50 || r.Servings != 4 {
		t.Errorf("times %d/%d, servings %d", r.PrepMin, r.CookMin, r.Servings)
	}
	if !slices.Equal(r.Ingredients, []string{"250 g red lentils", "1 onion, finely chopped", "2 tbsp oil"}) {
		t.Errorf("ingredients %q", r.Ingredients)
	}
	var steps []string
	for _, s := range r.Steps {
		steps = append(steps, s.Text)
	}
	if !slices.Equal(steps, []string{"Rinse the lentils.", "Fry the onion in the oil.", "Simmer for 20 minutes."}) {
		t.Errorf("steps %q", steps)
	}
	if !slices.Equal(r.Keywords, []string{"Dinner", "Indian", "vegan", "quick"}) {
		t.Errorf("keywords %q", r.Keywords)
	}
	if strings.Contains(p.Text, "Home Recipes") || strings.Contains(p.Text, "©") || strings.Contains(p.Text, ".x{}") ||
		!strings.Contains(p.Text, "Red lentil dal\nServes 4.") {
		t.Errorf("text %q", p.Text)
	}
}

func TestParsePageInstructionVariants(t *testing.T) {
	for name, tc := range map[string]struct{ jsonld, want string }{
		"html string": {
			`{"@type":"Recipe","name":"A","recipeIngredient":["x"],"recipeInstructions":"<p>One.</p><p>Two.</p>"}`,
			"One.|Two.",
		},
		"lines": {
			`{"@type":"Recipe","name":"A","recipeIngredient":"x","recipeInstructions":["One.","  ","Two."]}`,
			"One.|Two.",
		},
		"raw newlines in strings": {
			"{\"@type\":\"Recipe\",\"name\":\"A\",\"recipeIngredient\":[\"x\"],\"recipeInstructions\":\"One.\nTwo.\"}",
			"One. Two.",
		},
		"link-only step": {
			`{"@type":"Recipe","name":"A","recipeIngredient":["x"],"recipeInstructions":["One.","<a href=\"/video\">Watch the video</a>",{"@type":"HowToStep","text":"<p>See <a href=\"/tip\">the tip</a> and stir.</p>"}]}`,
			"One.|See the tip and stir.",
		},
		"mainEntity": {
			`{"@type":"WebPage","mainEntity":{"@type":"http://schema.org/Recipe","name":"A","ingredients":["x"],"recipeInstructions":[{"text":"One."}]}}`,
			"One.",
		},
	} {
		p := parse(t, "text/html", `<script type="application/ld+json">`+tc.jsonld+`</script>`)
		if p.Recipe == nil {
			t.Errorf("%s: no recipe", name)
			continue
		}
		var got []string
		for _, s := range p.Recipe.Steps {
			got = append(got, s.Text)
		}
		if strings.Join(got, "|") != tc.want {
			t.Errorf("%s: steps %q, want %q", name, got, tc.want)
		}
	}
}

// metaPage carries its recipe as microdata in meta elements, with nested
// items whose names must not leak into the recipe and a yield pulled in by
// itemref.
const metaPage = `<!doctype html><html lang="de"><body>
<ol itemscope itemtype="https://schema.org/BreadcrumbList">
 <li itemprop="itemListElement" itemscope itemtype="https://schema.org/ListItem"><span itemprop="name">Start</span></li></ol>
<main itemscope itemtype="https://schema.org/Recipe" itemref="servings">
 <span itemprop="author" itemscope itemtype="https://schema.org/Person"><meta itemprop="name" content="Chef"></span>
 <meta itemprop="keywords" content="Auflauf, Klassiker,">
 <meta itemprop="recipeCategory" content="Hauptspeise">
 <h1 itemprop="name">K&auml;sespätzle</h1>
 <meta itemprop="totalTime" content="PT45M">
 <img itemprop="image" src="/img/spaetzle.jpg" alt="">
 <meta itemprop="recipeIngredient" content="400 g Sp&auml;tzle">
 <meta itemprop="recipeIngredient" content="200 g Bergkäse">
 <ol>
  <li itemprop="recipeInstructions" itemscope itemtype="http://schema.org/HowToStep"><span itemprop="text">Spätzle
    <strong>kochen</strong>.</span></li>
  <li itemprop="recipeInstructions" itemscope itemtype="http://schema.org/HowToStep"><span itemprop="text">Mit Käse schichten.</span></li>
  <li itemprop="recipeInstructions" itemscope itemtype="http://schema.org/HowToStep"><span itemprop="text"><a href="/video">Zum Video</a></span></li>
 </ol>
</main>
<div id="servings"><meta itemprop="recipeYield" content="4"></div>
</body></html>`

// inlinePage carries its recipe as microdata on visible text.
const inlinePage = `<div itemscope itemtype="http://schema.org/Recipe">
 <h2 itemprop="https://schema.org/name">Pancakes</h2>
 <span itemprop="recipeYield">Serves 2</span>
 <time itemprop="prepTime" datetime="PT10M">10 minutes</time><time itemprop="cookTime">PT15M</time>
 <ul><li itemprop="recipeIngredient">200 g <a href="/flour">flour</a></li><li itemprop="recipeIngredient">2 eggs</li></ul>
 <div itemprop="recipeInstructions">
  <p>Whisk everything.</p>
  <p>Fry in
     <b>butter</b>.</p>
 </div>
</div>`

func TestParsePageMicrodata(t *testing.T) {
	steps := func(r *ai.Recipe) string {
		var out []string
		for _, s := range r.Steps {
			out = append(out, s.Text)
		}
		return strings.Join(out, "|")
	}

	r := parse(t, "text/html", metaPage).Recipe
	if r == nil {
		t.Fatal("meta page: no recipe")
	}
	if r.Name != "Käsespätzle" || r.ImageURL != "https://food.example/img/spaetzle.jpg" || r.CookMin != 45 || r.Servings != 4 {
		t.Errorf("meta page: recipe %+v", r)
	}
	if !slices.Equal(r.Ingredients, []string{"400 g Spätzle", "200 g Bergkäse"}) {
		t.Errorf("meta page: ingredients %q", r.Ingredients)
	}
	// The link-only step points elsewhere and is dropped.
	if got := steps(r); got != "Spätzle kochen.|Mit Käse schichten." {
		t.Errorf("meta page: steps %q", got)
	}
	if !slices.Equal(r.Keywords, []string{"Hauptspeise", "Auflauf", "Klassiker"}) {
		t.Errorf("meta page: keywords %q", r.Keywords)
	}

	r = parse(t, "text/html", inlinePage).Recipe
	if r == nil {
		t.Fatal("inline page: no recipe")
	}
	if r.Name != "Pancakes" || r.Servings != 2 || r.PrepMin != 10 || r.CookMin != 15 {
		t.Errorf("inline page: recipe %+v", r)
	}
	if !slices.Equal(r.Ingredients, []string{"200 g flour", "2 eggs"}) {
		t.Errorf("inline page: ingredients %q", r.Ingredients)
	}
	if got := steps(r); got != "Whisk everything.|Fry in butter." {
		t.Errorf("inline page: steps %q", got)
	}

	// JSON-LD wins over microdata on the same page.
	r = parse(t, "text/html", inlinePage+`<script type="application/ld+json">{"@type":"Recipe","name":"Crêpes","recipeIngredient":["x"]}</script>`).Recipe
	if r == nil || r.Name != "Crêpes" {
		t.Errorf("both: recipe %+v", r)
	}
}

func TestParsePageWithoutRecipe(t *testing.T) {
	for name, body := range map[string]string{
		"no json-ld":                    `<p>Just a blog post.</p>`,
		"other type":                    `<script type="application/ld+json">{"@type":"Article","name":"A"}</script>`,
		"invalid":                       `<script type="application/ld+json">{"@type":"Recipe",</script>`,
		"no name":                       `<script type="application/ld+json">{"@type":"Recipe","recipeIngredient":["x"]}</script>`,
		"deep nesting":                  `<script type="application/ld+json">` + strings.Repeat(`{"@graph":[`, 50) + `{"@type":"Recipe","name":"A","recipeIngredient":["x"]}` + strings.Repeat(`]}`, 50) + `</script>`,
		"microdata without ingredients": `<div itemscope itemtype="https://schema.org/Recipe"><h1 itemprop="name">A</h1></div>`,
		"microdata in a nested item":    `<div itemscope itemtype="https://schema.org/Recipe"><h1 itemprop="name">A</h1><div itemscope itemtype="https://schema.org/HowToSection"><span itemprop="recipeIngredient">x</span></div></div>`,
		"microdata itemref cycle":       `<div id="a" itemscope itemtype="https://schema.org/Recipe" itemref="b"><div id="b" itemprop="author" itemscope itemref="a"></div></div>`,
	} {
		if p := parse(t, "text/html", body); p.Recipe != nil {
			t.Errorf("%s: recipe %+v", name, p.Recipe)
		}
	}
}

func TestParsePageCharsetAndText(t *testing.T) {
	// "Grüße" in ISO-8859-1.
	p := parse(t, "text/html; charset=iso-8859-1", "<p>Gr\xfc\xdfe</p>")
	if p.Text != "Grüße" {
		t.Errorf("text %q", p.Text)
	}
	p = parse(t, "text/plain", "Dal\n\n1 cup lentils")
	if p.Text != "Dal\n\n1 cup lentils" || p.Recipe != nil {
		t.Errorf("plain text page %+v", p)
	}
	long := parse(t, "text/html", "<p>"+strings.Repeat("é", ai.MaxTextBytes)+"</p>")
	if len(long.Text) > ai.MaxTextBytes || !strings.HasSuffix(long.Text, "é") {
		t.Errorf("long text: %d bytes", len(long.Text))
	}
}
