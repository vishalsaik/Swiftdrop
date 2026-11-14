# Changelog

## Unreleased

### New

- **A cart that is kept, and a cart page with its own checkout.** The customer's cart is now kept in the browser (one per signed-in account, so a reload or a second tab still has it; if the browser will not store it the cart still works and says it is not kept). It holds one restaurant at a time: adding from another restaurant asks **Start a new cart?** and the cart is never emptied unseen. The cart button opens a page with the lines (steppers and Remove), where to deliver (a saved address, a search, where I am, or typed in), how to pay (card, wallet or cash), the total from the menu prices shown, and **Place order**. The order sent holds only menu item ids and counts, so the server still prices it; a refusal is shown in the server's own words (closed, sold out, too far, a field it would not take). One press places one order, even if pressed twice or retried after a dropped connection (the same order number is kept until it is placed), and a placed order empties the cart and opens in Orders.

- **A Food home and restaurant pages.** The customer's Food tab now shows where the food goes, the food search and a card for every open restaurant (name, cuisine, and once an address is chosen, how far it is and whether it delivers there, nearest first, by the same rule the server applies when the order is placed). A card opens the restaurant's page: the whole menu with prices, descriptions and a Veg pill, a search box and a vegetarian-only filter that work on the page, and steppers (0 to 50) that fill the cart. A restaurant that closed after the list loaded says it is not taking orders. **Add** in the search results opens the restaurant with one of the item in the cart, unless the cart holds another restaurant (then the page warns first). The cart sheet lists what is in it with a total from the menu prices shown, and **Check out** opens the order sheet filled from it (restaurant, what is still on the menu, the chosen address); a placed order empties the cart. (The cart was first held in memory; the entry above describes where it is kept now.)

- **A customer view.** A signed-in customer now gets their own app instead of a cut-down dispatch dashboard: **Food**, **Orders** (newest first, with the order drawer limited to what a customer may do) and **Account** tabs, and a cart button in the header. Staff keep the dashboard unchanged. Placing an order takes the customer to Orders with it open. The Food tab still starts an order with the order sheet; the restaurant page, the cart and the order tracker are the next items on the roadmap. The cart is a placeholder held in memory behind one hook.

- **Delivery addresses.** Every order says where it is going, and the server checks that its restaurant exists, has said where it is and delivers that far (a restaurant that has not been placed takes no delivery orders). An order keeps its own copy of the address. Customers save up to ten addresses and use them again (`/addresses`); staff placing a phone order choose or type one.
- **Address search.** `GET /geocode` and `/geocode/reverse` turn typed words into places and a place into words, through OpenStreetMap's Nominatim (or any service that speaks it) or a built-in demo list, chosen by `GEOCODER`; cached for an hour, limited to 30 a minute per person, and the key (`GEOCODER_KEY`) is never logged. The order sheet has a **Deliver to** chooser: saved, search, where I am, or type it in; the food search follows the chosen address.

- **Find food near you.** Restaurants have a place (latitude, longitude), a delivery radius and a cuisine, and items can be vegetarian. `GET /search` finds items by name, description, restaurant or cuisine (forgiving a typing mistake in an item's name), filters by vegetarian and price, and with the customer's position keeps only restaurants that deliver there, nearest first. The order sheet has a **Find food** box with a **Use my location** button: the browser is asked only when it is pressed, the position is rounded to about 110 metres, used for that search and kept nowhere, and refusing it leaves search by name working. Staff place a restaurant under **Menu** (or `PUT /restaurants/{id}/place`) and mark items vegetarian.
- The demo seed now has three restaurants, placed in Bengaluru and Mumbai, with cuisines and vegetarian flags; running it again places demo restaurants that had no place and changes nothing else.

### Changed

- The schema gains `pg_trgm`, restaurant place columns and `menu_items.veg`, and then an `addresses` table and `orders.delivery_address` (all applied in place by `migrate`).
- **`POST /orders` now needs `deliveryAddressId` or `deliveryAddress`**, and a restaurant that has not been placed refuses delivery orders (a `422`). Clients that placed orders without an address must send one.
- CORS allows `PUT` and `DELETE`.
- The Docker stack defaults to `GEOCODER=demo`; a plain `go run ./cmd/api` has address search off until `GEOCODER` is set.

## v1.0.0

The first complete version: the whole journey from signing up to a delivered order, runnable with one command.

### New

- **Menus, and orders priced from them.** Staff keep restaurants and menu items (**Menu** in the dashboard, or `/restaurants` and `/menu` by API). A customer orders by menu item id and count; the server takes the name and price from the menu, so nobody can order a burger for a cent. A customer cannot place a line that is not on the menu; staff can (a phone order), with a price of their own. Closed restaurants, sold-out items, items of another restaurant, more than 50 of one item and more than 30 lines are refused with a message that says which. An order keeps the name and price it was placed at, so a menu change never rewrites an order.
- **The order sheet picks from the menu.** A restaurant picker, the menu with steppers and a live total, and for staff a customer picker (or a walk-in) and an optional custom line.
- **`docker compose up --build`** starts Postgres (not published), creates the tables and starts the server on `127.0.0.1:8080`. The image is a small static build that runs as a non-root user.
- **`cmd/migrate`** creates or upgrades the tables (the schema is embedded in the binary, applied under a database lock so several can start at once, and safe to repeat). **`cmd/seed`** adds a small demo menu and never changes anything that is there.
- **`LISTEN_ADDR`** sets where the server listens (default `127.0.0.1:8080`; the image uses `0.0.0.0:8080`).
- **CI** (`.github/workflows/ci.yml`): gofmt, vet and every test with the race detector against Postgres; the dashboard's type check and tests, and a check that the committed build in `web/static` is what the source builds; then the whole Docker stack with the end-to-end test.
- **An end-to-end test** (`e2e/`) that walks the product over HTTP against a running stack, with no mocks.

### Changed

- `POST /orders` from a customer now takes `menuItemId` lines instead of names and prices (a customer's old request with its own prices is refused with `422`). Staff may still send custom lines.
- The sample orders in the dashboard are placed from the first open restaurant's menu.
- CORS allows `PATCH`.
- Three Go files were reformatted (gofmt) so CI can check formatting.

### Carried over from before v1

Payments, refunds, the ledger, alerts and their notifications, the reconciler, sign-in and accounts, and the dashboard. See the README.

### Known limits

See [KNOWN_ISSUES.md](KNOWN_ISSUES.md#v1-what-it-is-not): stand-in drivers, no email, staff-reset passwords, plain menus, no TLS in the Docker stack, and a CI workflow that GitHub has not run yet.
