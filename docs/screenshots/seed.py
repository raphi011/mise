#!/usr/bin/env python3
"""Fills a fresh mise instance with demo content for the README screenshots.

    MISE_DATA_DIR=$(mktemp -d) MISE_LISTEN=:8084 go run ./cmd/mise-server serve
    python3 docs/screenshots/seed.py http://localhost:8084

It talks to the running server like a browser would (setup, recipe editor,
planner, shopping list), so it needs a database that has not been set up yet.
Recipe photos are drawn with Pillow; install it with `pip install pillow`, or
the recipes are created without photos. Sign in afterwards as
demo / demo-password-123.
"""

import datetime
import io
import re
import sys
import urllib.error
import urllib.parse
import urllib.request
import uuid

BASE = (sys.argv[1] if len(sys.argv) > 1 else "http://localhost:8084").rstrip("/")
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


def photo(colors):
    """A simple plate: background, a plate, and a few food blobs."""
    try:
        from PIL import Image, ImageDraw, ImageFilter
    except ImportError:
        return None
    im = Image.new("RGB", (900, 675), colors[0])
    d = ImageDraw.Draw(im)
    d.ellipse((150, 60, 750, 640), fill=(245, 242, 235))
    d.ellipse((190, 100, 710, 600), fill=(236, 231, 221))
    spots = [(330, 260, 90), (470, 230, 80), (420, 380, 100), (560, 360, 70), (320, 420, 60), (520, 470, 55)]
    for i, (x, y, r) in enumerate(spots):
        d.ellipse((x - r, y - r, x + r, y + r), fill=colors[1 + i % (len(colors) - 1)])
    im = im.filter(ImageFilter.GaussianBlur(2))
    out = io.BytesIO()
    im.save(out, "JPEG", quality=82)
    return out.getvalue()


RECIPES = [
    dict(name="Sticky tofu with rice", servings=2, prep=10, cook=25, tags="Vegan, Asian, Weeknight",
         ing=[("400 g", "Firm tofu"), ("3 tbsp", "Soy sauce"), ("2 tbsp", "Maple syrup"), ("1 tbsp", "Rice vinegar"),
              ("200 g", "Jasmine rice"), ("2", "Spring onions")],
         steps=[("Press the tofu for 10 minutes, then cut it into cubes.", "10"),
                ("Whisk soy sauce, maple syrup and vinegar.", ""),
                ("Fry the tofu until golden, pour in the glaze and let it turn sticky.", "8"),
                ("Serve over rice with sliced spring onions.", "")],
         colors=[(214, 196, 160), (201, 140, 70), (120, 160, 80), (240, 235, 220), (180, 90, 50)]),
    dict(name="Tomato soup with basil", servings=4, prep=10, cook=30, tags="Vegetarian, Soup",
         ing=[("800 g", "Tinned tomatoes"), ("1", "Onion"), ("2", "Garlic cloves"), ("500 ml", "Vegetable stock"),
              ("1 bunch", "Basil"), ("100 ml", "Cream")],
         steps=[("Soften the onion and garlic in oil.", "6"), ("Add tomatoes and stock and simmer.", "20"),
                ("Blend with the basil and stir in the cream.", "")],
         colors=[(196, 214, 190), (200, 60, 40), (220, 90, 60), (90, 140, 70), (240, 230, 210)]),
    dict(name="Lemon spaghetti", servings=2, prep=5, cook=15, tags="Vegetarian, Pasta, Weeknight",
         ing=[("200 g", "Spaghetti"), ("1", "Lemon"), ("40 g", "Parmesan"), ("2 tbsp", "Olive oil"), ("1 handful", "Rocket")],
         steps=[("Cook the spaghetti in salted water.", "10"), ("Toss with lemon zest, juice, oil and parmesan.", ""),
                ("Top with rocket.", "")],
         colors=[(232, 214, 170), (240, 215, 110), (250, 235, 150), (110, 160, 80), (230, 200, 90)]),
    dict(name="Chickpea curry", servings=4, prep=15, cook=30, tags="Vegan, Curry",
         ing=[("2 tins", "Chickpeas"), ("400 ml", "Coconut milk"), ("1", "Onion"), ("2 tbsp", "Curry paste"),
              ("200 g", "Spinach"), ("300 g", "Basmati rice")],
         steps=[("Fry the onion and curry paste.", "5"), ("Add chickpeas and coconut milk and simmer.", "20"),
                ("Stir in the spinach and serve with rice.", "")],
         colors=[(222, 190, 130), (214, 150, 40), (190, 110, 30), (80, 140, 60), (245, 235, 210)]),
    dict(name="Blueberry pancakes", servings=4, prep=10, cook=20, tags="Vegetarian, Breakfast",
         ing=[("250 g", "Flour"), ("2", "Eggs"), ("300 ml", "Milk"), ("150 g", "Blueberries"), ("2 tbsp", "Butter")],
         steps=[("Whisk flour, eggs and milk into a batter.", ""), ("Fold in the blueberries.", ""),
                ("Fry small pancakes in butter until golden.", "15")],
         colors=[(214, 200, 220), (200, 150, 90), (80, 80, 150), (230, 190, 120), (240, 230, 215)]),
    dict(name="Greek salad", servings=2, prep=10, cook=0, tags="Vegetarian, Salad, Quick",
         ing=[("4", "Tomatoes"), ("1", "Cucumber"), ("150 g", "Feta"), ("1 handful", "Olives"), ("1", "Red onion")],
         steps=[("Chop the vegetables into chunks.", ""), ("Top with feta and olives, dress with oil and oregano.", "")],
         colors=[(200, 220, 200), (210, 60, 50), (120, 180, 90), (240, 240, 230), (90, 60, 90)]),
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
        img = photo(r["colors"])
        files = {"photo_file": ("photo.jpg", img)} if img else None
        url, _ = post("/recipes", fields, files)
        ids[r["name"]] = re.search(r"/recipes/([0-9a-f-]{36})", url).group(1)

    # Plan this week: dinner for most days, ingredients of the first two on the list.
    today = datetime.date.today()
    monday = today - datetime.timedelta(days=today.weekday())
    week = [("Sticky tofu with rice", 0), ("Lemon spaghetti", 1), ("Chickpea curry", 2),
            ("Tomato soup with basil", 4), ("Blueberry pancakes", 5)]
    for name, offset in week:
        rid = ids[name]
        _, page = get(f"/recipes/{rid}")
        ing = sorted(set(re.findall(rf"/recipes/{rid}/ingredients/([0-9a-f-]{{36}})/toggle", page)))
        date = (monday + datetime.timedelta(days=offset)).isoformat()
        post("/plan", {"recipe": rid, "date": date, "servings": "2", "ingredient": ing,
                       "back": f"/plan?week={monday.isoformat()}"})

    # A few loose entries on the shopping list.
    url, page = get("/groceries")
    lid = re.search(r"/groceries/lists/([0-9a-f-]{36})", url + page).group(1)
    for text in ["1 l oat milk", "6 eggs", "Bananas", "2 lemons", "Dishwasher tabs"]:
        post(f"/groceries/lists/{lid}/entries", {"text": text})
    print("seeded; sign in as demo /", PASSWORD)


try:
    main()
except urllib.error.HTTPError as e:
    sys.exit(f"{e.code} {e.url}: {e.read().decode()[:400]}")
