# Zillow request discovery notes

This document records the current transport, session, parsing, and reliability
boundaries of `gozillo`. Examples intentionally use placeholders rather than a
particular search, address, account, or captured browser identity.

## Result-page transport

Location mode fetches Zillow search-results-page HTML and extracts:

```text
script#__NEXT_DATA__
└── props.pageProps.searchPageState
    └── cat1.searchResults.listResults
```

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
variables. There is no external fetch process, browser runtime, automatic proxy
rotation, or CAPTCHA-solving integration.

State-qualified city input such as `Example City ST` is normalized to a stable
route. Returned listing addresses are checked against an explicit state suffix
so an ambiguous city name cannot silently mix another state or country into the
results.

## Session bootstrap and lifetime

A clean HTTP client may be rejected even when a browser can load the same page.
`gozillo session import` reads a user-supplied raw HAR and stores only cookies
that were actually sent to successful first-party Zillow requests. It does not
persist the HAR, response bodies, authorization headers, or browser identity.

Imported sessions are best-effort:

- cookies may be short-lived;
- cookies may be tied to the source IP, browser family, TLS fingerprint,
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

Browser versions can move ahead of the profiles bundled by `tls-client`.
Selecting the nearest profile in the same browser family is best-effort; exact
header/profile agreement improves compatibility but does not guarantee access
or disable adaptive protections.

Controlled compatibility testing showed that these layers are independently
significant: fresh cookies plus a browser-family TLS profile and User-Agent may
still be challenged when expected navigation headers are missing. Adding the
matching non-credential client hints and navigation headers can change the
outcome. This is evidence about request coherence, not a stable bypass or a
guarantee that the same identity will remain accepted.

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

Successful location responses and property details can be cached. Failed or
challenged locations are not cached, so repeating overlapping commands may
request the same failing route again. Retries and several independent filter
passes can therefore amplify traffic rather than improve coverage.

For a session-sensitive or large workflow:

1. import a fresh session;
2. use one multi-location process;
3. request an inclusive candidate set once;
4. use conservative location/detail delays and bounded concurrency;
5. disable retries when challenged routes should not be replayed;
6. split, rank, and deduplicate the JSON/JSONL output locally.

This structure preserves cookie evolution and avoids repeating the same search
page for separate bedroom, price, or amenity views.

Location mode currently reads one rendered result page per city or ZIP. It does
not paginate and does not invoke separate server-side property-type, bedroom,
or newest-result routes. A successful request should therefore not be treated
as exhaustive inventory.

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

Some active pages are community, building, or alternate landing pages and do
not expose the expected individual-property cache. Those responses are treated
as schema-unavailable rather than fabricated detail success.

## Structured rental facts

When an individual property object exposes `resoFacts`, the normalizer
conservatively derives:

- laundry features and in-unit/hookup/shared/none/unknown status;
- parking and private-garage signals;
- pet policy and allowed-pet text;
- availability, days on site, and year built;
- den, office, bonus room, loft/flex, and private-garage candidates.

Generic living rooms, dining areas, balconies, shared parking, and generic
leasing-office text are not promoted to private flex spaces. When a property
page is challenged or lacks the required property object, the listing is marked
unavailable and unknown facts do not satisfy strict filters.

## Capture boundary

No values copied from real captures—including cookies, authorization values,
client hints, browser fingerprints, CAPTCHA values, addresses, or tracking
identifiers—are stored in this repository. Checked-in HAR material must be
sanitized and contain request shape only. Real captures and generated search
results belong outside the repository.
