# gozillo

`gozillo` is a pure-Go Zillow CLI. Every live Zillow request uses an explicitly selected [`tls-client`](https://github.com/bogdanfinn/tls-client) browser profile; there is no production `net/http` transport fallback. The CLI parses search-result and property HTML in-process, supports HTTP/HTTPS/SOCKS5 proxies, and does not require a browser, Node.js, or the Browse CLI. Offline snapshot and local-file parsing do not require a TLS profile.

> Zillow's website endpoints are private and unsupported. They can change without notice. Use this tool conservatively, respect the site's terms and access controls, and do not use it to bypass login, CAPTCHA, rate limits, or other protections.

## Build

Requires Go 1.24.1 or newer.

```bash
go build -o gozillo ./cmd/gozillo
./gozillo --help
```

## HAR session bootstrap (no Keychain)

### Capture a non-sanitized HAR in Chrome

Chrome DevTools sanitizes HAR exports by default, removing headers such as `Cookie`, `Set-Cookie`, and `Authorization`. A sanitized export does not contain the cookie material required for session import. Follow Chrome's [Network panel HAR instructions](https://developer.chrome.com/docs/devtools/network/reference/#save-as-har), but explicitly enable the sensitive-data export:

1. Open the Zillow page in Chrome.
2. Open DevTools with **Command+Option+I** on macOS or **Ctrl+Shift+I** on Windows/Linux, then select the **Network** panel.
3. Open DevTools **Settings** using the gear icon or **F1**.
4. Under **Preferences > Network**, enable **Allow to generate HAR with sensitive data**. This is the required **non-sanitized** setting.
5. Return to **Network**, make sure recording is active, clear old requests, and remove any request filters. Enable **Preserve log** if the capture includes redirects or multiple navigations.
6. Reload the page and complete the normal browser flow until the target page has loaded successfully. Wait for the requests needed by the page to finish.
7. Open the **Export HAR** menu and choose **Export HAR (with sensitive data)**. Do not choose the sanitized export.
8. Save the file in a private location and import it promptly. For example:

   ```bash
   HAR="$HOME/Downloads/zillow.har"
   chmod 600 "$HAR"
   gozillo session import --name default "$HAR"
   ```

A non-sanitized HAR is a plaintext secret. It may contain active cookies, authorization material, account identifiers, browsing history, and request data. Never commit it, attach it to an issue, paste it into chat, or share it. Delete it after import when it is no longer required. The importer reads only successful first-party Zillow request cookies, but the raw HAR itself still contains everything Chrome exported.

For sites that accept a browser session but reject a clean HTTP client, import a **fresh** raw HAR captured from a successfully loaded Zillow page:

```bash
HAR=/path/to/fresh-zillow.har
SESSION=default
LOCATION='Example City ST'
TLS_PROFILE=chrome_146

gozillo session import --name "$SESSION" "$HAR"
gozillo session inspect --name "$SESSION"
gozillo search \
  --location "$LOCATION" \
  --rent \
  --session "$SESSION" \
  --tls-profile "$TLS_PROFILE"
```

The importer stores only cookies actually sent to successful first-party Zillow requests. It does **not** store authorization headers, response bodies, the raw HAR, or a browser User-Agent. Cookie values are never printed by `session list` or `session inspect`.

Legacy version-1 session files may still contain a `userAgent` field. It is accepted for backward compatibility but deliberately ignored. The default identity remains `gozillo/<version>`; any browser-shaped TLS profile, User-Agent, or navigation headers must be supplied explicitly by the caller.

No Keychain is used. Sessions are plaintext JSON secrets stored under the per-user config directory in a `0700` directory with `0600` files. Override the base directory with `GOZILLO_CONFIG_DIR`.

```bash
gozillo session list
gozillo session remove --name default
```

Delete the raw HAR after import if it is no longer needed. Session cookies may be short-lived or bound to an IP address, browser family, TLS fingerprint, navigation headers, or other browser state. Capture close to execution time when a clean session is rejected; there is no fixed lifetime that guarantees reuse.

Each network command loads the stored cookies into a new in-memory jar. `Set-Cookie` updates received during that command are available to later requests **inside the same process**, but they are not written back to the session file. A multi-location command therefore preserves cookie evolution across its locations and property-detail requests, while separate CLI invocations restart from the originally imported cookies. For session-sensitive workflows, prefer one broad multi-location command followed by local filtering instead of several overlapping network passes.

## Search by location

Use a ZIP or an explicitly state-qualified city:

```bash
LOCATION='Example City ST'
TLS_PROFILE=chrome_146

gozillo search \
  --location "$LOCATION" \
  --rent \
  --tls-profile "$TLS_PROFILE"
```

With an imported browser session:

```bash
gozillo search \
  --location "$LOCATION" \
  --rent \
  --session default \
  --tls-profile "$TLS_PROFILE"
```

Some networks or sessions may receive an `X-Px-Blocked` challenge. A proxy may be configured directly:

```bash
gozillo search \
  --location "$LOCATION" \
  --rent \
  --tls-profile "$TLS_PROFILE" \
  --proxy 'http://user:password@proxy.example:8080'
```

Or through Go's standard proxy environment variables:

```bash
export HTTPS_PROXY='http://user:password@proxy.example:8080'
gozillo search \
  --location "$LOCATION" \
  --rent \
  --tls-profile "$TLS_PROFILE"
```

Supported `--proxy` schemes:

- `http`
- `https`
- `socks5`
- `socks5h`

The proxy is used by the selected HTTP transport. There is no proxy rotation or external CLI invocation.

## Required `tls-client` transport

Every network-backed `search` or `property` command requires `--tls-profile`. Omitting it is a usage error; the CLI never silently falls back to Go's standard transport:

```bash
LOCATION='Example City ST'
TLS_PROFILE=chrome_146

gozillo search \
  --location "$LOCATION" \
  --rent \
  --tls-profile "$TLS_PROFILE"
```

Profile names are resolved case-insensitively from the version of `tls-client` linked into `gozillo`; `default` selects that library version's default profile. A missing or unknown profile is rejected before a request is made. The outer standard-library client still owns timeout, imported-cookie jar, Zillow host allowlist, redirect validation, and response-size/challenge processing, but all live wire traffic goes through `tls-client`.

A TLS profile does **not** automatically change the HTTP `User-Agent`: the default remains `gozillo/<version>`, and HAR imports still never replay a captured browser identity. Advanced users can set an explicit value with `--user-agent`, but an identity that does not match the selected TLS profile or the rest of the request can be rejected by the site.

For HTML navigation requests, advanced callers may also repeat `--browser-header 'Name: Value'` to supply an explicit allowlisted browser header such as `Accept-Language`, Chromium client hints, or `Sec-Fetch-*`. These headers are applied only to HTML requests. Credential-bearing and routing headers—including `Cookie`, `Authorization`, `User-Agent`, `Origin`, `Referer`, and `Host`—are rejected; cookies, User-Agent, origins, and referrers remain under gozillo's dedicated controls.

A coherent browser-shaped request may require all three explicit layers to agree: TLS profile, User-Agent, and non-credential navigation headers. For example:

```bash
CAPTURED_UA='...'

gozillo search \
  --location "$LOCATION" \
  --rent \
  --session default \
  --tls-profile "$TLS_PROFILE" \
  --user-agent "$CAPTURED_UA" \
  --browser-header 'Accept-Language: en-US,en;q=0.9' \
  --browser-header 'Sec-Fetch-Dest: document' \
  --browser-header 'Sec-Fetch-Mode: navigate'
```

Use only values obtained from an authorized, current browser session. Browser versions can advance beyond the profiles bundled by `tls-client`; selecting the nearest browser-family profile is best-effort and may still be rejected. Matching these layers improves compatibility but does not disable or bypass adaptive access controls.

The required TLS profile is a compatibility mechanism, not a CAPTCHA solver or permission to bypass access controls. Use it only where the target site's terms and authorization permit it.

## Multiple locations in one command

`--location` is repeatable and also accepts comma-separated values:

```bash
LOCATION_ONE='First City ST'
LOCATION_TWO='Second City ST'
MAX_RENT=3000

gozillo search \
  --location "$LOCATION_ONE" \
  --location "$LOCATION_TWO" \
  --rent \
  --session default \
  --tls-profile "$TLS_PROFILE" \
  --max-price "$MAX_RENT" \
  --min-baths 1 \
  --min-beds 2
```

Use an explicit state suffix for city names, for example `Example City ST`. Bare city names can resolve to another state or country. State-qualified searches discard listings whose returned address state does not match the requested state.

Repeated flags are equivalent:

```bash
gozillo search \
  --location "$LOCATION_ONE" \
  --location "$LOCATION_TWO" \
  --rent \
  --session default \
  --tls-profile "$TLS_PROFILE"
```

For multiple locations, table output includes an `AREA` column, JSON groups results by location, and JSONL emits `{location, listing}` records. `--limit` applies independently to each location.

For a session-sensitive workflow, put the full location set and an inclusive candidate filter in one invocation, then create narrower views from the JSONL locally. This preserves one cookie jar and avoids repeating the same routes in separate processes:

```bash
MIN_BEDS=2
MIN_BATHS=1
MAX_RENT=3500

gozillo --output=jsonl search \
  --location "$LOCATION_ONE" \
  --location "$LOCATION_TWO" \
  --rent \
  --session default \
  --tls-profile "$TLS_PROFILE" \
  --min-beds "$MIN_BEDS" \
  --min-baths "$MIN_BATHS" \
  --max-price "$MAX_RENT" \
  --sort-by newest \
  > broad-candidates.jsonl
```

Filtering or deduplicating `broad-candidates.jsonl` does not make additional Zillow requests.

## Filters

Price, bed, bath, square-footage, freshness, and availability filters work in
profile, snapshot, single-location, and multi-location modes:

```bash
gozillo --output=json search \
  --location "$LOCATION" \
  --rent \
  --session default \
  --tls-profile "$TLS_PROFILE" \
  --max-price 3000 \
  --min-beds 2 \
  --min-baths 1 \
  --min-sqft 800 \
  --max-days-on-zillow 14 \
  --available-from 2026-09-01 \
  --available-by 2026-10-01
```

Availability bounds are strict date bounds. Listings with missing or
unparseable availability are excluded when either bound is set.

### Structured rental-detail filters

Search cards do not consistently include laundry, parking, pet, or flex-room
facts. These filters therefore fetch each candidate property page with the same
pure-Go HTTP client and imported session, normalize Zillow's structured
`resoFacts`, and then filter locally:

```bash
gozillo --output=jsonl search \
  --location "$LOCATION_ONE" \
  --location "$LOCATION_TWO" \
  --rent \
  --session default \
  --tls-profile "$TLS_PROFILE" \
  --max-price 3500 \
  --min-beds 2 \
  --min-baths 1 \
  --laundry in-unit \
  --flex den,office,bonus,loft,private-garage \
  --unknown-laundry watchlist \
  --detail-workers 2
```

Supported detail values:

- `--laundry`: `any`, `in-unit`, `hookups`, `shared`, `none`, `unknown`
- `--parking`: `any`, `available`, `garage`, `private-garage`, `none`, `unknown`
- `--pets`: `any`, `allowed`, `dogs`, `cats`, `none`, `unknown`
- `--flex`: repeatable/comma-separated `den`, `office`, `bonus`, `loft`,
  `flex`, `private-garage`; requested values use OR matching

`--laundry`, `--parking`, `--pets`, and `--flex` automatically enable detail
enrichment. Use `--enrich-details` without one of those filters to retain all
listings while adding normalized `description`, `yearBuilt`, `laundry`,
`parking`, `petPolicy`, `allowedPets`, and `flexSpaces` fields.

By default, an unknown laundry result does not satisfy a laundry filter.
`--unknown-laundry watchlist` retains only those unknown-laundry candidates as
`matchStatus: "watchlist"`; a known conflicting value such as `hookups` is still
excluded. Community/apartment landing pages that do not expose an individual
property cache may therefore appear as watchlist entries rather than false
positive in-unit matches.

Detail fetching is bounded to 1-8 workers and defaults to 1, with a 750ms delay between request starts. Multi-location searches wait 2 seconds between live location requests and use bounded cooldown retries after a challenge or rate limit. They preserve successful locations and emit an error record for failed locations instead of discarding the whole command.

For session-sensitive or large searches, prefer one broad multi-location command, conservative `--location-delay` and `--detail-delay` values, and local post-filtering. This keeps one evolving cookie jar, avoids replaying failed routes in separate processes, and reduces request amplification. `--location-retries 0` is useful when a challenged route should not be retried automatically.

Location results are cached for 1 hour and property details for 6 hours under the owner-only `gozillo/cache` config directory. Successful responses can be reused by later filter views, but failed/challenged locations are not cached and may be requested again by another invocation. Use `--no-cache`, `--search-cache-ttl`, or `--property-cache-ttl` to control that behavior.

The CLI invokes no browser, Browse CLI, proxy service, CAPTCHA solver, or Keychain. The required `--tls-profile` transport and any explicit browser headers run in-process and do not solve a browser challenge. A location that remains challenged is represented explicitly as an error rather than a fabricated or silently missing result.

Location mode parses one rendered result page per city or ZIP. It does not currently paginate or invoke separate server-side property-type, bedroom, or newest-result routes. Use the returned metadata and direct-profile mode where appropriate rather than assuming one location request represents exhaustive site inventory.

### Sorting

Universal local sorting works in profile, snapshot, single-location, and
multi-location modes:

```bash
gozillo search \
  --location "$LOCATION_ONE" \
  --location "$LOCATION_TWO" \
  --rent \
  --session default \
  --tls-profile "$TLS_PROFILE" \
  --sort-by newest
```

Supported values:

- `recommended` (default; preserves Zillow order)
- `price-asc`
- `price-desc`
- `newest` (fewest `daysOnZillow`; unknown values last)
- `beds-desc`
- `sqft-desc`

For multiple locations, filtering, sorting, and `--limit` apply independently
within each location. `--limit` is applied after detail filters so matching
listings are not lost merely because earlier cards failed an amenity check.

`--page` and `--sort` currently require a derived direct-request profile.

## Offline snapshots

A saved Zillow results page or raw `__NEXT_DATA__` remains usable without a network request:

```bash
gozillo search --snapshot search.next.json --limit 20
gozillo search --snapshot results.html --max-price 4500
```

## Derived direct-request profiles

A sanitized HAR can be reduced to the current `/async-create-search-page-state` request format:

```bash
gozillo har sanitize --out search.sanitized.har search.raw.har
gozillo har candidates search.sanitized.har
gozillo har derive --out search.profile.json search.sanitized.har
gozillo search --profile search.profile.json --tls-profile "$TLS_PROFILE"
```

Direct profile requests may also receive `X-Px-Blocked`. Location mode fetches the rendered SRP HTML instead and supports the same standard proxy configuration.

The repository includes:

- [`examples/seattle-search.sanitized.har`](examples/seattle-search.sanitized.har)
- [`examples/seattle.search-profile.json`](examples/seattle.search-profile.json)

## Property details

From saved page HTML:

```bash
gozillo property ./property.html
```

From a URL, when ordinary direct access is allowed:

```bash
gozillo --output=json property \
  --session default \
  --tls-profile "$TLS_PROFILE" \
  'https://www.zillow.com/homedetails/.../123456_zpid/'
```

Property output includes normalized availability, days on Zillow, laundry,
parking, pet policy, allowed-pet text, and conservative flex-space signals when
those facts are present in the selected property object.

## Output modes

Global `--output` / `-o` values:

- `table` (default)
- `json`
- `jsonl`

## Safety and data handling

- Raw HAR files commonly contain cookies, authorization headers, search history, and personal data.
- `har sanitize` removes credential-bearing headers and cookie arrays, recursively sanitizes URL/JSON containers, and removes response bodies by default.
- Search profiles contain only the request template needed for one read-only operation.
- Proxy credentials are consumed by the selected in-process HTTP transport; `gozillo` does not persist them.
- `--browser-header` rejects credential-bearing and routing headers; its allowlisted values are not written into session files.
- Imported sessions are plaintext `0600` files by explicit design; no Keychain integration is used.
- Response cookies evolve only in memory for the current command and are not automatically written back to imported sessions.
- On Windows, commands that promise owner-only HAR/profile/session file permissions refuse the write rather than relying on ineffective POSIX mode bits; `har derive --out -` can still write a profile to stdout.
- No automatic CAPTCHA solving, browser control, Browse CLI dependency, credential replay, identity rotation, or proxy rotation is implemented. A TLS profile is mandatory for network requests but does not guarantee access.

## Development

```bash
gofmt -w .
go test ./...
go vet ./...
```

See [`docs/reverse-engineering.md`](docs/reverse-engineering.md) for the observed wire format and validation results.
