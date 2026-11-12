# Credits and data sources

## Nutrition data

The lookup of built-in values is in place, but `internal/catalog/data/nutrition.json` is still empty. Until it has rows, every nutrition value in mise is typed in by a household and there is nothing to credit.

The built-in ingredient values planned in [plan.md](plan.md) ("Nutrition") will come from the U.S. Department of Agriculture's FoodData Central (Foundation Foods and SR Legacy data types). The data are in the public domain under [CC0 1.0 Universal](https://creativecommons.org/publicdomain/zero/1.0/); USDA asks users to name FoodData Central as the source:

> U.S. Department of Agriculture, Agricultural Research Service. FoodData Central, 2019. fdc.nal.usda.gov.

- Source and licence statement: <https://fdc.nal.usda.gov/api-guide/> ("Licensing").
- Each row of the embedded table will keep its FoodData Central ID (`fdc_id`) as provenance.
- Values are estimates for raw ingredients and may differ from the products you buy. Rows edited by hand, where the source has no density or piece weight, will say so.

When the data ships, the nutrition block's footnote names USDA as well.
