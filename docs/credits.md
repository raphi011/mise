# Credits and data sources

## Nutrition data

The built-in ingredient values in `internal/catalog/data/nutrition.json` come from the U.S. Department of Agriculture's FoodData Central (Foundation Foods, release 2026-04-30, and SR Legacy, release 2018-04, the final one). The data are in the public domain under [CC0 1.0 Universal](https://creativecommons.org/publicdomain/zero/1.0/); USDA asks users to name FoodData Central as the source:

> U.S. Department of Agriculture, Agricultural Research Service. FoodData Central, 2019. fdc.nal.usda.gov.

- Source and licence statement: <https://fdc.nal.usda.gov/api-guide/> ("Licensing").
- 253 of the 263 catalogue entries have a row; each keeps its FoodData Central ID (`fdc_id`) as provenance. The rest (cleaning products, coffee, tea, vegetable stock, frozen berry mixes) have no raw USDA equivalent.
- Values are per 100 g of the raw food. Carbohydrate is the FoodData Central value minus fibre, the way EU labels give it. Values are estimates and may differ from the products you buy.
- Densities and piece weights come from the source's portion rows where it has one. Where it has none, the value was set by hand (bunches, packs, cans, whole fillets, European roll and baguette sizes). Those rows are marked `hand_edited` with a reason in `internal/catalog/gen/nutrition/nutrition_map.csv`.
- The file is generated: `go run ./internal/catalog/gen/nutrition <fdc-csv-dir>` reads the downloads (not committed) and that map.

The nutrition block's footnote and the public recipe page name USDA FoodData Central.
