# gozillo

<p align="center">
  <img src="assets/gozillo.png" alt="gozillo logo" width="240">
</p>

<p align="center">
  <strong>A pure-Go CLI for exploring Zillow listings.</strong>
</p>

`gozillo` searches locations, filters listings, fetches structured property
information, and prints table, JSON, or JSONL output. Live requests use a
[`tls-client`](https://github.com/bogdanfinn/tls-client) browser profile and can
reuse cookies from a fresh browser HAR capture.

> Zillow's website endpoints are private and unsupported. They may change
> without notice. Use the tool conservatively and respect the site's terms,
> rate limits, and access controls.

## Features

- Search one or many cities and ZIP codes with bounded pagination and pacing.
- Filter and sort by price, known monthly cost, beds, baths, availability,
  freshness, home type, and listing details.
- Expand apartment-community pages into available units or floor plans.
- Compare a previous run to identify new, changed, and still-active listings.
- Reuse browser-derived sessions and HTTP(S) or SOCKS proxies.
- Parse saved HTML or `__NEXT_DATA__` snapshots without network access.

## Install

Requires Go 1.24.1 or newer.

```bash
make build
./gozillo --help
```

## Quick start

Live searches require a compatible `tls-client` profile. When browser state is
needed, you can also reuse a fresh session and matching browser identity.

Start a dedicated Edge instance with CDP enabled on loopback:

```bash
make edge-cdp
```

Use that dedicated browser to establish a successful Zillow session you are
authorized to use. Then capture a new navigation and import its first-party
cookies:

```bash
HAR="$HOME/Downloads/zillow.raw.har"

./gozillo har capture \
  --cdp http://127.0.0.1:9222 \
  --out "$HAR" \
  'https://www.zillow.com/'

./gozillo session import --name default "$HAR"
```

Run a search with the captured browser identity:

```bash
LOCATION='Example City ST'
TLS_PROFILE='<working tls-client profile>'
CAPTURED_UA='<User-Agent from the successful browser request>'

./gozillo search \
  --location "$LOCATION" \
  --rent \
  --session default \
  --tls-profile "$TLS_PROFILE" \
  --user-agent "$CAPTURED_UA"
```

A raw HAR is a plaintext secret. Never commit, upload, paste, or share it, and
delete it when it is no longer needed. See the
[browser session guide](docs/browser-session.md) for Chrome and Edge setup,
manual HAR export, browser headers, privacy, and troubleshooting.

## Common usage

Search multiple locations in one process so they share the same in-memory
cookie jar:

```bash
./gozillo search \
  --location 'First City ST' \
  --location 'Second City ST' \
  --rent \
  --session default \
  --tls-profile "$TLS_PROFILE" \
  --sort-by newest
```

Parse a saved snapshot without a TLS profile:

```bash
./gozillo search --snapshot search.next.json --limit 20
./gozillo property ./property.html
```

Set the output mode before the command:

```bash
./gozillo --output=table search ...
./gozillo --output=json search ...
./gozillo --output=jsonl search ...
```

For deeper discovery, filtering, property verification, proxies, streaming
output, and history comparisons, see the [search guide](docs/search-guide.md).

## Commands

| Command | Purpose |
| --- | --- |
| `search` | Search locations, profiles, or saved snapshots |
| `property` | Read normalized property details |
| `session` | Import, inspect, list, and remove sessions |
| `har` | Capture HARs through CDP, sanitize them, and derive search profiles |
| `version` | Print the CLI version |

Run `./gozillo <command> --help` for command usage, options, and available
subcommands.

## Documentation

- [Browser session guide](docs/browser-session.md): CDP setup, HAR capture,
  sanitization and import, browser identity, privacy, and connection
  troubleshooting.
- [Search guide](docs/search-guide.md): discovery routes, filters, property
  details, proxies, offline snapshots, output, and coverage troubleshooting.
- [Request discovery notes](docs/request-discovery.md): transport, session,
  parsing, privacy, and reliability invariants for contributors.

## Security and privacy

- Session files and raw HARs are plaintext secrets stored with owner-only
  permissions on supported systems.
- Browser identity is not imported automatically, and cookie values are not
  printed by session inspection commands.
- Keep CDP endpoints on loopback unless you deliberately opt into and trust an
  exact remote WebSocket endpoint.
- CDP capture records traffic; it does not bypass authentication, rate limits,
  bot protections, or other access controls.
- Use only session state and destinations that you are authorized to access.
- Delete raw captures and remove imported sessions when they are no longer
  needed.

## Development

```bash
make check
```

Use `make help` to list focused build, formatting, test, race, vet, and data
boundary targets. Run `make ci` when dependencies need to be downloaded first.
