#!/usr/bin/env python3
"""Fills a fresh mise instance with demo content for the README screenshots.

    MISE_DATA_DIR=$(mktemp -d) MISE_LISTEN=127.0.0.1:8090 go run ./cmd/mise-server serve
    python3 docs/screenshots/seed.py http://127.0.0.1:8090

It talks to the running server like a browser would (setup, recipe editor,
planner, shopping list), so it needs a database that has not been set up yet.
Recipe photos come from docs/screenshots/photos (credits in CREDITS.md).
Sign in afterwards as
demo / demo-password-123.
"""

import datetime
import pathlib
import re
import sys
import urllib.error
import urllib.parse
import urllib.request
import uuid

BASE = (sys.argv[1] if len(sys.argv) > 1 else "http://127.0.0.1:8090").rstrip("/")
PASSWORD = "demo-password-123"

jar = urllib.request.HTTPCookieProcessor()
opener = urllib.request.build_opener(jar)


def post(path, fields, files=None):
    """POSTs a form (multipart when files are given) and returns (url, body)."""
    if files:
        boundary = uuid.uuid4().hex
        body = b""
        for key, values in fields.items():
            for v in values if isinstance(values, list) else [values]:
                body += f'--{boundary}\r\nContent-Disposition: form-data; name="{key}"\r\n\r\n{v}\r\n'.encode()
        for key, (name, data) in files.items():
            body += (f'--{boundary}\r\nContent-Disposition: form-data; name="{key}"; filename="{name}"\r\n'
                     "Content-Type: image/jpeg\r\n\r\n").encode() + data + b"\r\n"
        body += f"--{boundary}--\r\n".encode()
        ctype = f"multipart/form-data; boundary={boundary}"
    else:
        body = urllib.parse.urlencode(fields, doseq=True).encode()
        ctype = "application/x-www-form-urlencoded"
    req = urllib.request.Request(BASE + path, body, {"Content-Type": ctype})
    with opener.open(req) as res:
        return res.geturl(), res.read().decode()


def get(path):
    with opener.open(BASE + path) as res:
        return res.geturl(), res.read().decode()


PHOTOS = pathlib.Path(__file__).resolve().parent / "photos"

RECIPES = [
    dict(name="Greek salad", photo="greek-salad", servings=4, prep=15, cook=0, tags="Vegetarian, Salad, Summer",
         ing=[("4", "Ripe tomatoes"), ("1", "Cucumber"), ("1", "Red onion"), ("1", "Green pepper"),
              ("200 g", "Feta"), ("100 g", "Kalamata olives"), ("4 tbsp", "Olive oil"), ("1 tbsp", "Red wine vinegar"),
              ("1 tsp", "Dried oregano")],
         steps=[("Cut the tomatoes into wedges and the cucumber into thick half-moons. Slice the onion and pepper into thin rings.", ""),
                ("Pile everything into a wide bowl with the olives and let it sit for ten minutes so the tomatoes give up some juice.", "10"),
                ("Whisk the oil and vinegar, pour it over, and lay the feta on top in one slab. Sprinkle with oregano and serve with bread.", "")]),
    dict(name="Shakshuka", photo="shakshuka", servings=4, prep=10, cook=30, tags="Vegetarian, Breakfast, One pan",
         ing=[("2 tbsp", "Olive oil"), ("1", "Onion"), ("1", "Red pepper"), ("3", "Garlic cloves"),
              ("2 tsp", "Smoked paprika"), ("1 tsp", "Ground cumin"), ("800 g", "Tinned chopped tomatoes"),
              ("6", "Eggs"), ("100 g", "Feta"), ("1 handful", "Parsley")],
         steps=[("Soften the diced onion and pepper in the oil over medium heat until sweet and floppy.", "8"),
                ("Stir in garlic, paprika and cumin for a minute, then add the tomatoes with a pinch of salt. Simmer until thick enough to hold a spoon trail.", "15"),
                ("Make six hollows in the sauce and crack an egg into each. Cover and cook until the whites are just set and the yolks still wobble.", "6"),
                ("Crumble feta over the top, scatter parsley and bring the pan to the table with warm bread.", "")]),
    dict(name="Chickpea curry", photo="chickpea-curry", servings=4, prep=15, cook=30, tags="Vegan, Curry, Weeknight",
         ing=[("2 tbsp", "Vegetable oil"), ("1", "Onion"), ("3", "Garlic cloves"), ("1 piece", "Ginger, thumb-sized"),
              ("2 tbsp", "Curry powder"), ("2 tins", "Chickpeas"), ("400 g", "Tinned chopped tomatoes"),
              ("200 ml", "Coconut milk"), ("150 g", "Baby spinach"), ("300 g", "Basmati rice")],
         steps=[("Fry the chopped onion in the oil until golden, then add grated garlic and ginger and cook for a minute.", "10"),
                ("Add the curry powder and toast it briefly, then pour in the tomatoes, drained chickpeas and coconut milk.", ""),
                ("Simmer uncovered until the sauce clings to the chickpeas. Cook the rice alongside.", "20"),
                ("Fold in the spinach until wilted, taste for salt and lemon, and serve over rice.", "")]),
    dict(name="Lemon spaghetti", photo="lemon-spaghetti", servings=2, prep=5, cook=15, tags="Vegetarian, Pasta, Quick",
         ing=[("200 g", "Spaghetti"), ("2", "Unwaxed lemons"), ("40 g", "Butter"), ("1", "Garlic clove"),
              ("50 g", "Parmesan"), ("2 handfuls", "Rocket"), ("1 pinch", "Chilli flakes")],
         steps=[("Boil the spaghetti in well-salted water and keep a mugful of the cooking water before draining.", "10"),
                ("Melt the butter with the crushed garlic, the zest of both lemons and the juice of one.", ""),
                ("Toss the pasta through the butter with a splash of pasta water and most of the parmesan until glossy.", ""),
                ("Stir in the rocket, top with the remaining parmesan and chilli, and eat straight away.", "")]),
    dict(name="Blueberry pancakes", photo="blueberry-pancakes", servings=4, prep=10, cook=20, tags="Vegetarian, Breakfast, Sweet",
         ing=[("250 g", "Plain flour"), ("2 tbsp", "Sugar"), ("2 tsp", "Baking powder"), ("1 pinch", "Salt"),
              ("300 ml", "Milk"), ("2", "Eggs"), ("30 g", "Melted butter"), ("150 g", "Blueberries"),
              ("4 tbsp", "Maple syrup")],
         steps=[("Mix the flour, sugar, baking powder and salt in a large bowl.", ""),
                ("Whisk the milk, eggs and melted butter, pour into the dry mix and stir only until the lumps are mostly gone.", ""),
                ("Ladle small rounds into a lightly buttered pan, drop a few berries onto each and cook until bubbles appear on top, then flip.", "3"),
                ("Stack the pancakes, add the rest of the berries and pour over the syrup.", "")]),
    dict(name="Tonkotsu-style ramen", photo="ramen", servings=2, prep=15, cook=20, tags="Asian, Soup, Comfort food",
         ing=[("800 ml", "Chicken stock"), ("2 tbsp", "White miso"), ("1 tbsp", "Soy sauce"), ("2 cloves", "Garlic"),
              ("2 nests", "Ramen noodles"), ("2", "Eggs"), ("2", "Spring onions"), ("1 tbsp", "Sesame oil"),
              ("1 sheet", "Nori"), ("1 tsp", "Chilli oil")],
         steps=[("Lower the eggs into boiling water for seven minutes, cool them in cold water, then peel and halve.", "7"),
                ("Warm the stock with the crushed garlic, whisk in the miso and soy sauce, and keep it just below a simmer.", "10"),
                ("Boil the noodles as the packet says, drain well and divide between two deep bowls.", "4"),
                ("Ladle the broth over, then add the eggs, sliced spring onions, nori, sesame and chilli oil.", "")]),
    dict(name="Roast chicken with garlic", photo="roast-chicken", servings=4, prep=15, cook=90, tags="Meat, Sunday, Roast",
         ing=[("1.8 kg", "Whole chicken"), ("1 head", "Garlic"), ("1", "Lemon"), ("50 g", "Soft butter"),
              ("4 sprigs", "Thyme"), ("800 g", "Potatoes"), ("2", "Carrots"), ("2 tbsp", "Olive oil")],
         steps=[("Heat the oven to 200 C. Pat the chicken dry and rub it all over with butter, salt and pepper.", ""),
                ("Put the halved lemon, thyme and half the garlic inside the bird and set it in a roasting tin on the chopped potatoes and carrots tossed with oil.", ""),
                ("Roast, scattering the remaining garlic cloves around the tin after the first half hour.", "75"),
                ("Check that the juices from the thigh run clear, then rest the chicken under foil before carving.", "15")]),
    dict(name="Tiramisu", photo="tiramisu", servings=6, prep=30, cook=0, tags="Vegetarian, Dessert, Make ahead",
         ing=[("3", "Egg yolks"), ("60 g", "Sugar"), ("250 g", "Mascarpone"), ("200 ml", "Double cream"),
              ("250 ml", "Strong espresso, cooled"), ("2 tbsp", "Marsala"), ("200 g", "Savoiardi biscuits"),
              ("2 tbsp", "Cocoa powder")],
         steps=[("Whisk the yolks and sugar over a pan of barely simmering water until pale and thick, then take off the heat and let cool.", "8"),
                ("Beat in the mascarpone, then fold in the cream whipped to soft peaks.", ""),
                ("Stir the marsala into the espresso and dip the biscuits briefly, one at a time, to line a dish.", ""),
                ("Spread half the cream over the biscuits, add a second layer of each, and smooth the top.", ""),
                ("Chill overnight, then dust with cocoa just before serving.", "")]),
]


def main():
    post("/setup", dict(household="Home", timezone="Europe/Vienna", week_start="mon", base_url=BASE,
                        name="Demo", username="demo", email="demo@example.com", password=PASSWORD))
    ids = {}
    for r in RECIPES:
        n = len(r["ing"])
        fields = {
            "name": r["name"], "servings": str(r["servings"]), "prep_min": str(r["prep"]), "cook_min": str(r["cook"]),
            "new_tags": r["tags"], "ing_key": [f"n{i}" for i in range(n)],
            "ing_amount": [a for a, _ in r["ing"]], "ing_name": [b for _, b in r["ing"]],
            "step": [s for s, _ in r["steps"]], "step_timer": [t for _, t in r["steps"]],
        }
        files = {"photo_file": ("photo.jpg", (PHOTOS / f"{r['photo']}.jpg").read_bytes())}
        url, _ = post("/recipes", fields, files)
        ids[r["name"]] = re.search(r"/recipes/([0-9a-f-]{36})", url).group(1)

    # Plan this week; the ingredients of the first two meals go on the list.
    today = datetime.date.today()
    monday = today - datetime.timedelta(days=today.weekday())
    week = [("Chickpea curry", 0), ("Lemon spaghetti", 1), ("Tonkotsu-style ramen", 2), ("Shakshuka", 3),
            ("Greek salad", 4), ("Blueberry pancakes", 5), ("Roast chicken with garlic", 6), ("Tiramisu", 6)]
    servings = {r["name"]: r["servings"] for r in RECIPES}
    for i, (name, offset) in enumerate(week):
        rid = ids[name]
        _, page = get(f"/recipes/{rid}")
        ing = sorted(set(re.findall(rf"/recipes/{rid}/ingredients/([0-9a-f-]{{36}})/toggle", page))) if i < 2 else []
        date = (monday + datetime.timedelta(days=offset)).isoformat()
        post("/plan", {"recipe": rid, "date": date, "servings": str(servings[name]), "ingredient": ing,
                       "back": f"/plan?week={monday.isoformat()}"})

    # One recipe gets a public link (the share page screenshot).
    post(f"/recipes/{ids['Shakshuka']}/share", {})

    # A few loose entries on the shopping list.
    url, page = get("/groceries")
    lid = re.search(r"/groceries/lists/([0-9a-f-]{36})", url + page).group(1)
    for text in ["1 l oat milk", "Bananas", "Dishwasher tabs", "Kitchen roll"]:
        post(f"/groceries/lists/{lid}/entries", {"text": text})
    print("seeded; sign in as demo /", PASSWORD)


try:
    main()
except urllib.error.HTTPError as e:
    sys.exit(f"{e.code} {e.url}: {e.read().decode()[:400]}")
