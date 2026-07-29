# Browser session setup

Live `gozillo` requests require an explicit `tls-client` profile. When browser
state is needed, a fresh HAR can provide first-party Zillow cookies and the
browser identity used by a successful request.

- [Capture through CDP](#capture-through-cdp)
- [Export a HAR manually](#export-a-har-manually)
- [Import the session](#import-the-session)
- [Configure browser identity](#configure-browser-identity)
- [Sanitize a capture and derive a profile](#sanitize-a-capture-and-derive-a-profile)
- [Session privacy and lifecycle](#session-privacy-and-lifecycle)
- [Troubleshooting](#troubleshooting)

## Capture through CDP

The quickest way to start a dedicated Edge instance is:

```bash
make edge-cdp
# or: ./scripts/open-edge-cdp.sh
```

Override the defaults with `GOZILLO_CDP_PORT` or
`GOZILLO_CDP_PROFILE_DIR` when needed.

Start Chrome or Edge with a remote-debugging port and a **dedicated,
non-default** user data directory. Remote debugging grants control of that
browser instance, so keep the endpoint on loopback and do not expose port
`9222` to another machine. Non-loopback endpoints are rejected unless
`--allow-remote-cdp` is supplied explicitly. Remote capture requires an exact
`ws://` or `wss://` browser WebSocket URL; HTTP discovery remains loopback-only.

### macOS

```bash
# Chrome
"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" \
  --remote-debugging-port=9222 \
  --remote-debugging-address=127.0.0.1 \
  --user-data-dir="$HOME/.gozillo/chrome-cdp"

# Edge
"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge" \
  --remote-debugging-port=9222 \
  --remote-debugging-address=127.0.0.1 \
  --user-data-dir="$HOME/.gozillo/edge-cdp"
```

### Linux

Adjust the executable name for your installation:

```bash
# Chrome
google-chrome \
  --remote-debugging-port=9222 \
  --remote-debugging-address=127.0.0.1 \
  --user-data-dir="$HOME/.gozillo/chrome-cdp"

# Edge
microsoft-edge \
  --remote-debugging-port=9222 \
  --remote-debugging-address=127.0.0.1 \
  --user-data-dir="$HOME/.gozillo/edge-cdp"
```

Use the dedicated browser normally to establish only the session state you are
authorized to use. Then capture one new navigation:

```bash
HAR="$HOME/Downloads/zillow.raw.har"

./gozillo har capture \
  --cdp http://127.0.0.1:9222 \
  --out "$HAR" \
  'https://www.zillow.com/'
```

The command opens a new tab, starts CDP network recording, and then navigates to
the supplied URL. CDP cannot recover requests that occurred before recording
began, so it cannot reconstruct an already-loaded tab's past traffic.

### Capture controls

- `--wait <duration>` keeps recording after page load, with a default of `5s`.
- `--timeout <duration>` bounds the complete capture, with a default of `45s`,
  and must be longer than `--wait`.
- `--response-bodies` attempts to save response bodies. It is off by default
  because bodies can make the HAR much larger and may contain more sensitive
  data. Redirect-hop bodies may be unavailable after CDP reuses a request ID;
  those entries are marked incomplete.
- Capture retains at most 10,000 entries and 128 MiB of event and body data. It
  fails instead of writing a partial HAR if either limit is exceeded.

The recorder targets the primary page session, which contains the first-party
navigation and fetch/XHR traffic used by `gozillo`. Requests emitted only from
child CDP targets, such as dedicated workers or out-of-process iframes, may be
omitted. The HAR log records this limitation in its comment metadata.

`gozillo` writes raw HARs atomically with owner-only mode `0600` on supported
systems. Owner-only HAR writes are currently unsupported on Windows; use the
manual export flow there and protect the resulting file appropriately.

## Export a HAR manually

Chrome and Edge omit cookies and authorization data from sanitized HAR exports.
Session import needs the **non-sanitized** export:

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

See the official
[Microsoft Edge Network reference](https://learn.microsoft.com/en-us/microsoft-edge/devtools/network/reference#save-all-network-requests-to-a-har-file)
and
[Chrome Network reference](https://developer.chrome.com/docs/devtools/network/reference/#save-as-har)
for the browser export flow.

Save the capture somewhere private:

```bash
HAR="$HOME/Downloads/zillow.har"
chmod 600 "$HAR"
```

A non-sanitized HAR is a plaintext secret. Never commit, upload, paste, or share
it. Delete it when it is no longer needed. CDP capture is a recording mechanism,
not a way to bypass authentication, rate limits, bot protections, or other
access controls.

## Import the session

```bash
./gozillo session import --name default "$HAR"
./gozillo session inspect --name default
```

The importer stores first-party Zillow cookies from successful requests. It
does not store the HAR, response bodies, authorization headers, or browser
User-Agent.

## Configure browser identity

A TLS profile, User-Agent, and browser navigation headers are separate parts of
the request. Keep the User-Agent and client hints exactly as captured from a
successful browser request. Do not rewrite them to match the TLS profile name.

Start with the nearest family profile—Chrome-family for Edge or Chrome,
Safari-family for Safari, and Firefox-family for Firefox. Browser releases may
be newer than the bundled profiles, so treat profile selection as a
compatibility test. If the first profile is rejected, keep the captured
User-Agent and headers unchanged while testing another profile.

```bash
LOCATION='Example City ST'
TLS_PROFILE='<working tls-client profile>'
CAPTURED_UA='<User-Agent from the successful browser request>'
SEC_CH_UA='<Sec-CH-UA value from the successful browser request>'

./gozillo search \
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

## Sanitize a capture and derive a profile

Use a sanitized copy when inspecting request shapes or deriving a reusable
direct-search profile:

```bash
./gozillo har sanitize --out search.sanitized.har "$HAR"
./gozillo har candidates search.sanitized.har
./gozillo har derive --out search.profile.json search.sanitized.har
```

Sanitization removes known cookies, credential headers, sensitive values, and
response bodies. Review the sanitized output before sharing or committing it;
keep the original raw HAR private.

## Session privacy and lifecycle

- Session files are plaintext secrets stored with owner-only permissions.
- Cookie values are never printed by `session list` or `session inspect`.
- Browser identity is not imported automatically.
- Response cookies live only for the current command and are not written back to
  the session file.
- Use one multi-location command when cookie continuity matters.
- Remove sessions when finished:

  ```bash
  ./gozillo session remove --name default
  ```

## Troubleshooting

### `--tls-profile is required`

Every live search or property request needs an explicit browser profile. Offline
snapshots and local HTML do not.

### `X-Px-Blocked`

Try a fresh HAR from a successful browser page load. Keep the captured
User-Agent and allowlisted navigation headers exact. Start with the nearest
browser-family TLS profile; if it is rejected, test another profile while
changing only that variable. Avoid repeating the same failing routes across
many separate commands.

See the [search guide](search-guide.md) for search behavior, filters, and
coverage troubleshooting.
