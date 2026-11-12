# Icon experiments: verdict

Test set: lemon, onion, garlic, flour (flour is the "abstract pantry item" case). `index.html` renders every file at 24 px (plain and on a category tile) and 48 px (on a tile), using the tokens and `.tile`/`.avatar--*` classes from `internal/ui/static/css/app.css`. View it with `python3 -m http.server` from the repo root, then `/docs/icon-experiments/index.html?theme=light|dark`. Checked in the browser pane at 375 px and 1024 px, both themes.

| | hand/ | generated/ | emoji/ |
|---|---|---|---|
| Style | 24 px grid, 1.75 stroke, `currentColor`, 20 % fill | flat multi-colour fills, fixed hex | platform glyph |
| Theming | takes the tile's `--av-fg`, so both themes work | fixed colours: garlic (cream) vanishes on the light sand tile, onion pink clashes with the palette | fine on both |
| 24 px | readable; onion and garlic differ only by a base nub | most legible, colour carries identity | most legible |
| 48 px | consistent, quiet | consistent among themselves, loud next to the app's flat tiles | photographic, different look per OS |
| Bytes (4 files) | 2.1 KB | 2.4 KB | 0.2 KB per reference, 0 for the glyph |
| Copyright | us (author); licence is ours to pick | no human author: likely not copyrightable in the US, so nobody can stop copies and we cannot stop them either; Anthropic's terms assign us the output | codepoints are free; glyph art belongs to Apple, Google, Microsoft and is only rendered by the OS font, never shipped by us |

Findings
- F1 Hand and generated are the same act here: the SVG came from a model either way. The real choice is style system, not who typed the path. A strict mono style (`currentColor` + 20 % fill) beats multi-colour because it inherits the tile colour and survives dark mode without per-icon work.
- F2 Silhouette-alike foods collide in mono: onion and garlic needed a second pass and are still close. Lemon needed one revision (round first draft read as an onion; pointed tips and a tilt fixed it). Expect 30-40 % of icons to need a second pass.
- F3 Emoji has the best recognition and zero cost, but covers roughly 100-130 of 400 items (no flour, baking powder, cumin, soy sauce, most cheeses), and looks different per OS. A bundled open emoji set (Noto Emoji Apache-2.0, Fluent Emoji MIT; OpenMoji is CC BY-SA, Twemoji CC BY: attribution or share-alike, avoid) fixes the look, not the coverage. Licences quoted from memory: confirm before bundling.
- F4 Generated multi-colour needs a theme variant per icon or a rule (always draw on a light chip); not worth it.

Scaling to 300-500 items (mono style)
- Authoring: one model pass at about 450 tokens per icon is 200-250k output tokens for 500, a few dollars. Review is the cost: contact-sheet render in both themes, about 1 min per icon, 8-10 h, plus ~35 % redo. Pure hand drawing at 15-25 min per icon is 120-200 h: not realistic.
- Size: 500 icons at about 0.55 KB is 275 KB raw, roughly 70-90 KB gzipped as one sprite. Loaded once, cached.
- Not every item needs a unique icon. Cluster by silhouette (about 150-200 icons for 400 items: all hard cheeses share one, all pastas one, herbs one). That halves the cost and the review.
- Fallback for items without an icon stays the existing category-coloured initial tile; mixing emoji into a mono set looks broken.

Recommendation: mono duotone `currentColor` icons, model-drafted from a written style spec, human-reviewed on a contact sheet, clustered to 150-250 SVGs shipped as one sprite. Decided: mono `currentColor`, no emoji (plan D2, D5).
