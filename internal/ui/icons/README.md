# Icon sources

`mise run icons` renders the PNG icons in `../static/img/` with ImageMagick:

- `icon-192.png`, `icon-512.png` from `../static/img/icon.svg` (rounded, for "any" purpose);
- `icon-maskable-512.png` and `apple-touch-icon.png` (180 px) from `maskable.svg`, full bleed with the mark inside the maskable safe zone. iOS rounds the corners itself.

The PNGs are committed; rerun the task after changing either SVG.
