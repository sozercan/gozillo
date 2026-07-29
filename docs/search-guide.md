# Search guide

This guide covers multi-location searches, bounded discovery, filters, property
verification, proxies, offline parsing, output, and coverage troubleshooting.
For session capture and browser identity setup, start with the
[browser session guide](browser-session.md).

The examples assume:

```bash
LOCATION='Example City ST'
TLS_PROFILE='<working tls-client profile>'
CAPTURED_UA='<User-Agent from the successful browser request>'
```

Use an explicitly state-qualified city name when possible. Live searches and
property requests require an explicit TLS profile; offline parsing does not. A
browser-derived session is optional and can be supplied when browser state is
needed.

## Multiple locations

```bash
./gozillo search \
  --location 'First City ST' \
  --location 'Second City ST' \
  --rent \
  --session default \
  --tls-profile "$TLS_PROFILE" \
  --sort-by newest
```

Put the full location set in one command so session-sensitive requests share the
same in-memory cookie jar.

## Bounded discovery

Location mode can bootstrap Zillow's rendered page and then use bounded
server-side result routes instead of treating the first Recommended page as
complete inventory:

```bash
PREVIOUS='./previous-results.jsonl'
TARGET_END='YYYY-MM-DD'

./gozillo --output=jsonl search \
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
depth while keeping one process and cookie jar.

Discovery merges Zillow list and map results. Supplemental no-laundry routes
cover listings whose in-unit laundry was not indexed, while exact-two-bedroom
keyword routes improve flex recall. Shared-room, co-living, dorm-style,
student-housing, and structured income-restriction signals can be excluded
after detail expansion. Strict boundary and allowed-city checks remove
cross-boundary results.

Community results are expanded from structured unit and floor-plan data.
Floor-plan-only or otherwise uncertain results remain watchlist items.

## Filters and property verification

```bash
./gozillo --output=jsonl search \
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
fetch detail pages. Apartment-community pages can produce multiple unit or
floor-plan results.

Use `--page-delay`, `--detail-delay`, and `--location-delay` to reduce request
frequency for larger searches. Add `--progress` for line-oriented location,
route/page, retry, and detail progress on stderr; stdout remains
machine-readable. Multi-location JSONL is flushed as each location completes.

After a challenge or rate limit, queued property-detail requests for that
location are skipped instead of continuing through the entire batch. Later
locations start with a fresh detail circuit.

When `--verify-recency` is enabled, post-expansion location checks and all
recency-independent detail filters run first. Unit-page recency requests are
limited to listings that can still survive the final result set;
`--max-days-on-zillow` and `--sort-by newest` still use verified values.

Set `GOZILLO_CACHE_DIR` to keep search and property caches separate from
`GOZILLO_CONFIG_DIR`. This is useful when sessions remain isolated per run but
cache entries should be reused across runs for their configured TTL.

## Proxy

```bash
./gozillo search \
  --location "$LOCATION" \
  --rent \
  --tls-profile "$TLS_PROFILE" \
  --proxy 'http://proxy.example:8080'
```

You can also use Go's standard `HTTPS_PROXY` and `NO_PROXY` environment
variables.

## Property details

Fetch a live property URL:

```bash
./gozillo --output=json property \
  --session default \
  --tls-profile "$TLS_PROFILE" \
  'https://www.zillow.com/homedetails/.../123456_zpid/'
```

Parse saved HTML without network access:

```bash
./gozillo property ./property.html
```

## Offline snapshots

Offline parsing does not require a TLS profile:

```bash
./gozillo search --snapshot search.next.json --limit 20
./gozillo search --snapshot results.html --max-price 3000
```

## Output

Set the global output mode before the command:

```bash
./gozillo --output=table search ...
./gozillo --output=json search ...
./gozillo --output=jsonl search ...
```

For multi-location JSONL, each record contains either a listing or a location
error:

```json
{"location":"Example City ST","listing":{}}
{"location":"Another City ST","error":"..."}
```

When available, listings include `requiredMonthlyFees`, `totalMonthlyCost`,
verification notes, `historyStatus`, and meaningful `historyChanges`.
Community-expanded units may not expose `daysOnZillow`.

`--verify-recency` performs a second, non-destructive unit-page pass. When an
individual unit page exposes the current rental cycle in `priceHistory`,
`gozillo` derives `listedDate`, `updatedDate`, and `daysOnZillow`. When neither
source exists, those recency fields remain unset instead of being inferred from
availability or page-render timestamps.

## Coverage troubleshooting

The default location search remains one rendered page. For deeper coverage,
use bounded `--bed-range`, `--server-sort`, `--max-pages`, and `--home-type`
routes. `--supplemental-no-laundry` covers amenity-index misses, and
`--keyword-route` adds focused flex searches.

Bounded location retries also apply to challenged or rate-limited route pages.
Review stderr coverage warnings when a route has more reported pages than the
configured cap. Zillow can still omit or change private website data.

Run `./gozillo search --help` and `./gozillo property --help` for the complete
option reference. Contributor-facing implementation details are in the
[reverse-engineering notes](reverse-engineering.md).
