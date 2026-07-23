# Zillow request discovery notes

This document records the current transport, session, parsing, and reliability
boundaries of `gozillo`. Examples intentionally use placeholders rather than a
particular search, address, account, or captured browser identity.

## Result-page transport

The default location mode fetches Zillow search-results-page HTML and extracts:

```text
script#__NEXT_DATA__
└── props.pageProps.searchPageState
    ├── queryState
    └── cat1.searchResults.listResults
```

Bounded discovery mode uses that rendered page as a bootstrap. It converts the
captured `queryState` into an in-memory search profile, then performs paginated
`PUT /async-create-search-page-state` requests for configured bedroom, sort,
and keyword routes. Each request asks for both `listResults` and `mapResults`,
which are normalized and deduplicated before cross-page/route deduplication.
Results are deduplicated across pages and routes while route-level
coverage records whether the configured page cap reached Zillow's reported end.
A challenged or rate-limited route page uses the command's bounded retry and
backoff policy without discarding earlier successful routes. If retries are
exhausted, the failure is retained as a partial-coverage issue, remaining routes
are skipped, and multi-location output includes both the earlier listings and an
incomplete-discovery error record.

Routes are constructed from a normalized location:

```text
For sale: https://www.zillow.com/homes/<LOCATION>_rb/
Rentals:  https://www.zillow.com/<LOCATION>/rentals/
```

Every network-backed CLI request requires an explicit `tls-client` profile;
there is no production standard-transport fallback. An outer Go `net/http.Client`
still coordinates cookies, redirects, timeout, and response handling, while the
wire transport uses `tls-client` for browser-family TLS and HTTP fingerprint
compatibility. Offline parsing does not require a profile.

A proxy can be configured through `--proxy` or Go's standard proxy environment
variables. Search and property commands do not launch an external fetch process
or browser runtime, and there is no automatic proxy rotation or CAPTCHA-solving
integration. `gozillo har capture` is separate: it attaches to a Chromium
browser that the user started explicitly through CDP.

State-qualified city input such as `Example City ST` is normalized to a stable
route. Returned listing addresses are checked against an explicit state suffix
so an ambiguous city name cannot silently mix another state or country into the
results.

## Session bootstrap and lifetime

A clean HTTP client may be rejected even when a browser can load the same page.
`gozillo session import` reads a user-supplied raw HAR and stores only cookies
that were actually sent to successful first-party Zillow requests. It does not
persist the HAR, response bodies, authorization headers, or browser identity.

### CDP-backed HAR capture

`gozillo har capture` can record a new navigation through an already-running
Chrome or Edge instance:

```bash
gozillo har capture \
  --cdp http://127.0.0.1:9222 \
  --out "$HOME/Downloads/zillow.raw.har" \
  'https://www.zillow.com/'
```

Start the browser with a loopback remote-debugging port and a dedicated,
non-default user data directory. For example, use `google-chrome` or
`microsoft-edge` on Linux (use the corresponding application executable on
macOS):

```bash
google-chrome \
  --remote-debugging-port=9222 \
  --remote-debugging-address=127.0.0.1 \
  --user-data-dir="$HOME/.gozillo/chrome-cdp"

microsoft-edge \
  --remote-debugging-port=9222 \
  --remote-debugging-address=127.0.0.1 \
  --user-data-dir="$HOME/.gozillo/edge-cdp"
```

The dedicated profile supplies its normal cookies and browser state to the new
tab. Remote debugging grants control of that browser instance, so the endpoint
must remain private; do not expose it to another machine. Non-loopback
endpoints require the explicit `--allow-remote-cdp` flag and an exact `ws://` or
`wss://` browser WebSocket URL; HTTP discovery is loopback-only. Use only
session state and destinations that you are authorized to access.

Capture allocates a new tab and enables CDP network recording before navigating
to the supplied HTTP(S) URL. That ordering ensures the initial navigation is in
the HAR. CDP cannot recover requests made before recording began, so the command
does not reconstruct traffic from an existing, already-loaded tab.

`--wait` controls how long recording continues after page load (default `5s`).
`--timeout` bounds the complete connection, navigation, and capture operation
(default `45s`) and must be greater than `--wait`. Response bodies are omitted by
default; `--response-bodies` attempts to include them when they are required,
at the cost of a larger and potentially more sensitive archive. Redirect-hop
bodies may be unavailable after CDP reuses a request ID and are marked as such.
Capture retains at most 10,000 entries and 128 MiB of event/body data; exceeding
either bound fails the command rather than writing a partial archive.
The recorder covers the primary page CDP target, including the main navigation
and page fetch/XHR traffic needed by `gozillo`; requests emitted only by child
targets such as dedicated workers or out-of-process iframes may be omitted. The
HAR log records that scope in comment metadata.

The output is an unsanitized HAR and can contain cookies, credential headers,
request bodies, URLs, and, when requested, response bodies. `gozillo` writes it
atomically with owner-only mode `0600` on supported systems. Owner-only HAR
writes are currently unsupported on Windows. Never commit, upload, paste, or
share a raw capture; sanitize or delete it as soon as it is no longer needed.
This capture path does not bypass authentication, rate limits, bot protections,
or other access controls.

Imported sessions are best-effort:

- cookies may be short-lived;
- cookies may be tied to the source IP, browser identity, TLS fingerprint,
  navigation headers, or other browser state;
- a capture that worked earlier may be rejected on its first later request;
- there is no universal age threshold that guarantees validity.

Capture close to execution time when a fresh browser session is required.
Session files are plaintext secrets and are stored only in an owner-only
configuration directory.

### Process boundary

Each CLI invocation loads the persisted cookies into a new in-memory cookie
jar. Response `Set-Cookie` updates are available to subsequent requests within
that process, including later locations, redirects, and property-detail
requests. They are deliberately **not** written back to the session file.

This distinction matters for session-sensitive workflows:

- one multi-location command shares an evolving jar for the whole command;
- several separate commands each restart from the original imported cookies;
- a broad request followed by offline/local filtering is preferable to several
  overlapping network passes when identity continuity matters.

Blind cookie writeback is intentionally avoided. An HTTP challenge can set
cookies before the application classifies the response as challenged; saving
those values automatically could poison a reusable session.

## Browser identity coherence

A browser-shaped TLS profile alone does not create a coherent browser request.
Servers may compare several layers:

1. TLS and HTTP protocol fingerprint;
2. HTTP `User-Agent`;
3. browser client hints and navigation headers;
4. cookies and their evolution during the session;
5. request timing, ordering, IP address, and other adaptive signals.

A TLS profile is mandatory for network requests, but the default HTTP identity
within that profile remains the honest `gozillo/<version>` User-Agent with
minimal headers. Browser identity is never imported or replayed automatically.
Advanced callers must explicitly supply additional browser-shaped layers:

```bash
gozillo search \
  --location "$LOCATION" \
  --rent \
  --session "$SESSION" \
  --tls-profile "$TLS_PROFILE" \
  --user-agent "$CAPTURED_UA" \
  --browser-header 'Accept-Language: en-US,en;q=0.9' \
  --browser-header 'Sec-Fetch-Dest: document' \
  --browser-header 'Sec-Fetch-Mode: navigate'
```

`--browser-header` is restricted to an explicit non-credential allowlist for
HTML navigation requests. Supported families include `Accept`,
`Accept-Language`, cache/navigation controls, Chromium client hints,
`Sec-Fetch-*`, `DNT`, `Sec-GPC`, and `Priority`.

The following remain under dedicated client control and cannot be supplied
through `--browser-header`:

- `Cookie` and authorization headers;
- `User-Agent`;
- `Origin` and `Referer`;
- `Host` and content-routing headers.

The captured User-Agent and client hints are independent from the selected TLS
profile. An Edge User-Agent includes a Chrome compatibility token while still
identifying itself as Edge. Selecting a Safari-named TLS profile does not change
that HTTP identity. Callers should preserve captured HTTP values exactly rather
than rewriting them to match the profile name.

Browser versions can move ahead of the profiles bundled by `tls-client`. The
nearest profile in the same browser family is a reasonable starting point, not
a strict requirement. Compatibility must be tested empirically while changing
one layer at a time. No profile choice guarantees access or disables adaptive
protections.

Controlled compatibility testing showed that these layers are independently
significant: fresh cookies plus a browser-shaped TLS profile and User-Agent may
still be challenged when expected navigation headers are missing. Adding the
captured non-credential client hints and navigation headers can change the
outcome. Conversely, more than one TLS profile can work with the same captured
HTTP identity. This is evidence about request compatibility, not a stable bypass
or a guarantee that the same identity will remain accepted.

## `tls-client` adapter

The mandatory network adapter bridges standard-library `net/http` requests to
`github.com/bogdanfinn/fhttp`, which is the request type used by `tls-client`.
It preserves:

- request context and cancellation;
- method, URL, body, trailers, and content length;
- response status, headers, body, trailers, and compression state;
- proxy and timeout configuration;
- outer standard-library cookie-jar behavior.

Redirects are disabled inside `tls-client`. The outer `net/http.Client` remains
responsible for redirect limits, Zillow-host validation, cookie processing, and
body replay.

Recognized browser navigation headers are emitted in a stable browser-like
order before unrecognized headers. TLS extension randomization is enabled for
Chrome- and Brave-family profiles. A `default` profile alias selects the
version of the default profile linked into the binary. Missing or unknown
profiles are rejected before a live request is attempted.

## Batch reliability and request volume

Multi-location searches are best-effort per location. Successful locations are
preserved when another location fails, and JSONL emits either:

```json
{"location":"<location>","listing":{}}
{"location":"<location>","error":"..."}
```

Successful location responses and property details can be cached. Search-cache
identity includes semantic route filters and page limits, but not pacing or retry
settings, so changing a delay does not force an otherwise identical request set.
Failed or incomplete locations are not cached.

`--progress` writes request-plan, location, route/page, retry-cooldown, and
property-detail progress to stderr. Multi-location JSONL is emitted as each
location completes. Discovery consumes the retry budget per bootstrap/page
request, does not replay the entire route matrix afterward, and stops remaining
routes when a retryable response exhausts that budget. A retryable
property-detail response opens a location-scoped circuit breaker so queued
detail and recency requests for that location are skipped. The circuit resets
before required detail enrichment begins for the next location.

Recency verification runs after community expansion, authoritative location
boundary checks, and every detail filter except `max-days-on-zillow`. Only the
surviving unit URLs receive the optional recency request; the max-days filter,
newest sort, limit, and history annotation remain afterward. `GOZILLO_CACHE_DIR`
can place the search/property cache outside a run-local `GOZILLO_CONFIG_DIR`,
allowing dated runs to share entries without sharing imported session files.

For a session-sensitive or large workflow:

1. import a fresh session;
2. use one multi-location process;
3. request an inclusive candidate set once;
4. use conservative location/detail delays and bounded concurrency;
5. use bounded cooldown retries in the same process instead of replaying separate filter passes;
6. split, rank, and deduplicate the JSON/JSONL output locally.

This structure preserves cookie evolution and avoids repeating the same search
page for separate bedroom, price, or amenity views.

Location mode defaults to one rendered result page for compatibility. Bounded
discovery is enabled by server-route flags such as `--bed-range`,
`--server-sort`, `--home-type`, and `--max-pages`. Supplemental routes can omit
Zillow's indexed laundry flag before local detail confirmation, and exact-two
keyword routes cover flex terms that may not appear in normalized room data.
Sort values can carry a
per-route cap (`days:3`), while `--location-max-pages` can reduce or increase
depth for selected locations within the same process. Search totals across
overlapping city and ZIP routes are not additive or unique, and a configured
page cap can still leave reported pages unread; coverage warnings make that
limitation explicit.

Optional strict boundary validation requires exact postal codes for ZIP routes
and exact city names for city routes. Query aliases support neighborhoods whose
postal city differs from the Zillow route name, while an allowed-city set removes
cross-boundary ZIP results after detail expansion.

## Internal direct-search request

The website application also uses a direct request shaped like:

```http
PUT https://www.zillow.com/async-create-search-page-state
Content-Type: application/json
```

with a body similar to:

```json
{
  "searchQueryState": {"...": "captured query state"},
  "wants": {"cat1": ["listResults"]},
  "requestId": 2,
  "isDebugRequest": false
}
```

`gozillo har derive` can create a sanitized direct-request profile for this
endpoint when a compatible HAR entry exists. Direct profile mode supports page
and raw sort parameters that location mode does not, but it can still receive
access-control or schema-drift responses.

## Property pages

Property pages normally include a `__NEXT_DATA__` JSON script. Within
`props.pageProps.componentProps.gdpClientCache`, one cache entry may contain the
individual property object. The property command selects the authoritative
property identity and normalizes that object from local HTML or a live URL.

Individual pages are parsed from the authoritative property object when it is
available. Apartment-community pages use a separate structured path under
`componentProps.initialReduxState.gdp.building`. Floor plans and units are
expanded into normalized rental properties with unit/floor-plan beds, baths,
base rent, required monthly fees, availability, laundry, parking, pets, and
Zillow URLs. A floor plan without an exact available unit is retained with a
verification note instead of being presented as fully confirmed. Community and
unit payloads frequently omit `daysOnZillow`. Optional recency verification
performs a second pass over expanded unit URLs without discarding already
confirmed facts when that pass fails. Individual unit pages can expose a
current rental cycle in `priceHistory`; the normalizer uses the most recent
post-removal “Listed for rent” event and subsequent rental updates to derive
listed/updated dates and exact calendar days. When neither source exists,
exporters preserve an explicit `Unknown` value with evidence and last-checked
fields rather than deriving age from availability or page-render timestamps. Other
alternate landing pages without either supported structure remain
schema-unavailable.

## Structured rental facts

When an individual property object exposes `resoFacts`, the normalizer
conservatively derives:

- laundry features and in-unit/hookup/shared/none/unknown status;
- base rent, known required monthly fees, and known total monthly cost;
- parking and private-garage signals;
- pet policy and allowed-pet text;
- availability, days on site, rental status, and year built;
- den, office, bonus room, loft/flex, and private-garage candidates;
- verification notes for floor-plan-only results, unknown fees, and garages
  whose exclusive use must be confirmed;
- structured and text-derived shared-room, co-living, dorm, individual-lease,
  per-bed, and student-housing signals for explicit exclusion;
- income-restricted, low-income, income-limit, and structured household
  eligibility signals for explicit exclusion.

Generic living rooms, dining areas, balconies, shared parking, and generic
leasing-office text are not promoted to private flex spaces. When a property
page is challenged or lacks the required property object, the listing is marked
unavailable and unknown facts do not satisfy strict filters.

## Local result history

`--previous-results` accepts prior JSON or JSONL emitted by `gozillo`, including
multi-location wrapper records. Current listings are matched by Zillow ID, then
URL, then normalized address and labeled as new, previously changed, or still
active. Meaningful changes include price, known total cost, required fees,
availability, bedroom/bathroom count, laundry confirmation, flex details, and
rental status. This comparison is local and never reads Gmail or another
service.

## Capture boundary

No values copied from real captures—including cookies, authorization values,
client hints, browser fingerprints, CAPTCHA values, addresses, or tracking
identifiers—are stored in this repository. Checked-in HAR material must be
sanitized and contain request shape only. Real captures and generated search
results belong outside the repository.
