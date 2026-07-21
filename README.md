# gozillo

<p align="center">
  <img src="assets/gozillo.png" alt="gozillo logo" width="240">
</p>

<p align="center">
  <strong>A pure-Go CLI for exploring Zillow listings.</strong>
</p>

`gozillo` searches locations, filters listings, fetches structured property
details, and prints clean table, JSON, or JSONL output. Live requests use a
[`tls-client`](https://github.com/bogdanfinn/tls-client) browser profile and can
reuse cookies from a fresh browser HAR capture.

> Zillow's website endpoints are private and unsupported. They may change
> without notice. Use the tool conservatively and respect the site's terms,
> rate limits, and access controls.

## Features

- Search one or many cities and ZIP codes.
- Run bounded server-side bedroom, home-type, Newest, and Recently Changed routes.
- Paginate each route with conservative request delays and per-location caps.
- Expand Zillow apartment-community pages into available units or floor plans.
- Filter by base rent, known total monthly cost, beds, baths, availability, and freshness.
- Verify laundry, parking, pets, status, fees, and flex-space details from Zillow pages.
- Compare with a previous JSON/JSONL run to label new, changed, and still-active listings.
- Sort locally by newest, price, beds, or square footage.
- Reuse a browser session from a fresh HAR capture.
- Use HTTP, HTTPS, SOCKS5, or SOCKS5H proxies.
- Work offline with saved HTML or `__NEXT_DATA__` snapshots.

## Install

Requires Go 1.24.1 or newer.

```bash
go build -o gozillo ./cmd/gozillo
./gozillo --help
```

## Quick start

### 1. Capture a non-sanitized HAR in Edge or Chrome

Edge and Chrome omit cookies and authorization data from sanitized HAR
exports. A session import needs the **non-sanitized** export:

1. Open Zillow in Microsoft Edge or Google Chrome and open DevTools:
   - macOS: **Command+Option+I**
   - Windows/Linux: **Ctrl+Shift+I**
2. Select the **Network** panel.
3. Open DevTools **Settings** with the gear icon or **F1**.
4. Under **Preferences > Network**, enable
   **Allow to generate HAR with sensitive data**.
5. Return to Network, clear old requests, remove filters, and reload the page.
6. Complete the normal browser flow and wait for the page to finish loading.
7. Open **Export HAR** and select **Export HAR (with sensitive data)**.

The same Chromium DevTools flow is documented in the
[Microsoft Edge Network reference](https://learn.microsoft.com/en-us/microsoft-edge/devtools/network/reference#save-all-network-requests-to-a-har-file)
and the
[Chrome Network reference](https://developer.chrome.com/docs/devtools/network/reference/#save-as-har).

Save the capture somewhere private:

```bash
HAR="$HOME/Downloads/zillow.har"
chmod 600 "$HAR"
```

A non-sanitized HAR is a plaintext secret. Never commit, upload, paste, or share
it. Delete it when it is no longer needed.

### 2. Import the session

```bash
gozillo session import --name default "$HAR"
gozillo session inspect --name default
```

The importer stores first-party Zillow cookies from successful requests. It
does not store the HAR, response bodies, authorization headers, or browser
User-Agent.

### 3. Search

Live requests require a `tls-client` profile. Start with the nearest profile
for the captured browser family, but treat profile selection as a compatibility
test rather than a strict browser-name match:

```bash
LOCATION='Example City ST'
TLS_PROFILE='<working tls-client profile>'
CAPTURED_UA='<User-Agent from the successful browser request>'

gozillo search \
  --location "$LOCATION" \
  --rent \
  --session default \
  --tls-profile "$TLS_PROFILE" \
  --user-agent "$CAPTURED_UA"
```

Use an explicitly state-qualified city name when possible. Add the matching
navigation headers described below when the captured browser sent them.

## Browser identity

A TLS profile, User-Agent, and browser navigation headers are separate parts
of the request. Keep the User-Agent and client hints exactly as captured from a
successful browser request. Do not rewrite them to match the TLS profile name.
For a Chromium-family capture, the additional flags may look like:

```bash
SEC_CH_UA='<Sec-CH-UA value from the successful browser request>'

gozillo search \
  --location "$LOCATION" \
  --rent \
  --session default \
  --tls-profile "$TLS_PROFILE" \
  --user-agent "$CAPTURED_UA" \
  --browser-header "Sec-CH-UA: $SEC_CH_UA" \
  --browser-header 'Accept-Language: en-US,en;q=0.9' \
  --browser-header 'Sec-Fetch-Dest: document' \
  --browser-header 'Sec-Fetch-Mode: navigate'
```

`--browser-header` accepts only non-credential navigation headers. Cookies,
authorization, User-Agent, origin, referrer, and host headers cannot be supplied
through it.

An Edge User-Agent contains a Chrome compatibility token but still identifies
itself as Edge, and its client hints may do the same. A Safari-named TLS profile
does not turn the captured User-Agent into Safari.

Start with the nearest family profile—Chrome-family for Edge or Chrome,
Safari-family for Safari, and Firefox-family for Firefox. Browser releases may
be newer than the bundled profiles, and the nearest profile is only a
best-effort starting point. If it is rejected, keep the captured User-Agent and
headers unchanged while testing another available profile.

## Search examples

### Multiple locations

```bash
gozillo search \
  --location 'First City ST' \
  --location 'Second City ST' \
  --rent \
  --session default \
  --tls-profile "$TLS_PROFILE" \
  --sort-by newest
```

For session-sensitive searches, put the full location set in one command so all
requests share the same in-memory cookie jar.

### Deep Zillow discovery

Location mode can bootstrap Zillow's rendered page and then use bounded
server-side result routes. This avoids treating the first Recommended page as
complete inventory:

```bash
PREVIOUS='./previous-results.jsonl'
TARGET_END='YYYY-MM-DD'

gozillo --output=jsonl search \
  --location 'First City ST' \
  --location 'Second City ST' \
  --rent \
  --session default \
  --tls-profile "$TLS_PROFILE" \
  --user-agent "$CAPTURED_UA" \
  --bed-range 2 \
  --bed-range 3+ \
  --server-sort days:3 \
  --server-sort mostrecentchange:1 \
  --supplemental-no-laundry \
  --supplemental-pages 1 \
  --keyword-route den \
  --keyword-route office \
  --keyword-route 'private garage' \
  --max-pages 3 \
  --page-delay 5s \
  --home-type apartment,condo,townhouse,single-family \
  --strict-location-boundary \
  --allowed-city 'First City,Second City' \
  --max-price 3500 \
  --max-total-cost 3500 \
  --min-baths 2 \
  --available-by "$TARGET_END" \
  --unknown-availability watchlist \
  --out-of-window-availability watchlist \
  --verify-rental-status \
  --verify-recency \
  --exclude-shared-housing \
  --exclude-student-housing \
  --exclude-income-restricted \
  --laundry in-unit \
  --unknown-laundry watchlist \
  --previous-results "$PREVIOUS" \
  --sort-by newest
```

A server sort may include its own page cap, such as `days:3`. Use
`--location-max-pages 'Priority City ST=3'` to give selected locations more
depth while keeping one process and cookie jar. Discovery merges Zillow list
and map results. Supplemental no-laundry routes cover listings whose in-unit
laundry was not indexed, while exact-two-bedroom keyword routes improve flex
recall. Shared-room, co-living, dorm-style, student-housing, and structured
income-restriction signals can be excluded after Zillow detail expansion. Strict boundary and allowed-city checks remove cross-boundary results.
Community results are expanded from Zillow's structured unit and floor-plan
data; floor-plan-only or otherwise uncertain results remain watchlist items.

### Filters and property details

```bash
gozillo --output=jsonl search \
  --location "$LOCATION" \
  --rent \
  --session default \
  --tls-profile "$TLS_PROFILE" \
  --max-price 3500 \
  --max-total-cost 3500 \
  --min-beds 2 \
  --min-baths 1 \
  --min-sqft 800 \
  --verify-rental-status \
  --verify-recency \
  --exclude-shared-housing \
  --exclude-student-housing \
  --exclude-income-restricted \
  --laundry in-unit \
  --unknown-laundry watchlist \
  --flex den,office,bonus,loft,private-garage \
  --sort-by newest
```

Laundry, total-cost, rental-status, parking, pet, and flex filters automatically
fetch Zillow detail pages. Apartment-community pages can produce multiple unit
or floor-plan results. Use `--page-delay`, `--detail-delay`, and
`--location-delay` to reduce request frequency for larger searches.

### Proxy

```bash
gozillo search \
  --location "$LOCATION" \
  --rent \
  --tls-profile "$TLS_PROFILE" \
  --proxy 'http://user:password@proxy.example:8080'
```

You can also use Go's standard `HTTPS_PROXY` and `NO_PROXY` environment
variables.

## Property details

From a live URL:

```bash
gozillo --output=json property \
  --session default \
  --tls-profile "$TLS_PROFILE" \
  'https://www.zillow.com/homedetails/.../123456_zpid/'
```

From saved HTML:

```bash
gozillo property ./property.html
```

## Offline snapshots

Offline parsing does not require a TLS profile:

```bash
gozillo search --snapshot search.next.json --limit 20
gozillo search --snapshot results.html --max-price 3000
```

## Output

Set the global output mode before the command:

```bash
gozillo --output=table search ...
gozillo --output=json search ...
gozillo --output=jsonl search ...
```

For multi-location JSONL, each record contains either a listing or a location
error:

```json
{"location":"Example City ST","listing":{}}
{"location":"Another City ST","error":"..."}
```

When available, listings also include `requiredMonthlyFees`,
`totalMonthlyCost`, verification notes, `historyStatus`, and meaningful
`historyChanges`. Community-expanded units may not expose `daysOnZillow`. `--verify-recency`
performs a second, non-destructive unit-page pass. When an individual unit page
exposes the current rental cycle in `priceHistory`, gozillo derives
listed/updated dates and exact calendar days; otherwise CSV exports label the
value `Unknown`, explain the evidence gap, and record the last-checked date.

## Commands

| Command | Purpose |
| --- | --- |
| `search` | Search locations, profiles, or saved snapshots |
| `property` | Read normalized property details |
| `session` | Import, inspect, list, and remove sessions |
| `har` | Sanitize HARs and derive direct-search profiles |
| `version` | Print the CLI version |

Run `gozillo <command> --help` for the full option list.

## Sessions and privacy

- Session files are plaintext secrets stored with owner-only permissions.
- Cookie values are never printed by `session list` or `session inspect`.
- Browser identity is not imported automatically.
- Response cookies live only for the current command and are not written back to
  the session file.
- Use one multi-location command when cookie continuity matters.
- Remove sessions when finished:

  ```bash
  gozillo session remove --name default
  ```

## Troubleshooting

### `--tls-profile is required`

Every live search or property request needs an explicit browser profile. Offline
snapshots and local HTML do not.

### `X-Px-Blocked`

Try a fresh HAR from a successful browser page load. Keep the captured
User-Agent and allowlisted navigation headers exact. Start with the nearest
browser-family TLS profile, but if it is rejected, test another profile while
changing only that one variable. Avoid repeating the same failing routes across
many separate commands.

### Missing listings

The default location search remains one rendered page. For deeper coverage,
use bounded `--bed-range`, `--server-sort`, `--max-pages`, and `--home-type`
routes. `--supplemental-no-laundry` covers amenity-index misses, and
`--keyword-route` adds focused flex searches. Review stderr coverage warnings
when a route has more reported pages than the configured cap. Zillow can still omit or change private website data.

## Development

```bash
gofmt -w .
go test ./...
go vet ./...
```

See [reverse-engineering notes](docs/reverse-engineering.md) for transport,
session, and parsing details.
