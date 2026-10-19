# Optional TRMNL private plugin

This adapter sends selected cached climate readings to TRMNL's cloud. It is disabled by default and needs no public listener. Enabling it explicitly shares those readings, chosen display names and timestamps with TRMNL. OAuth credentials, upstream home/zone IDs, original room names and unselected rooms are never included. A webhook URL is a bearer secret: keep it outside the repository and never paste it into issues, screenshots or logs.

## Private plugin and virtual device

You can test without physical hardware using a [TRMNL virtual device](https://help.trmnl.com/en/articles/12410512-what-is-a-virtual-device). Eligibility and claiming are managed by TRMNL; the documented virtual device is offered with qualifying hardware purchases and a developer license. An eligible existing account can create a private plugin and use the browser Markup Editor/preview. This repository does not create devices or change your TRMNL account.

1. In your TRMNL account, choose your virtual device and create a Private Plugin with the **Webhook** strategy. Save it to obtain its Webhook URL. Private plugins require the applicable Developer/BYOD entitlement.
2. Import the five files in this directory as a flat ZIP, or paste the four Liquid templates into the matching Markup Editor tabs. `settings.yml` configures a 15-minute render interval; this is separate from the bridge's 30-minute publishing interval.
   To update an already imported plugin, use **Edit Markup** and replace the matching viewport tabs with the new `.liquid` contents, then Save and Force Refresh. Importing the ZIP again creates another plugin; it does not update your existing webhook.

3. Use `example-payload.json` for the first integration test. It contains eight synthetic rooms, with absent targets/humidity, zero values and unknown heating states. Send it only to your own private plugin, using a secret environment variable:

   ```sh
   # Set TRMNL_WEBHOOK_URL privately in your shell first.
   curl --fail --silent --show-error --output /dev/null \
     --request POST --header 'Content-Type: application/json' \
     --data-binary @integrations/trmnl/example-payload.json \
     "$TRMNL_WEBHOOK_URL"
   ```

   This command makes one outbound request and shares only the synthetic fixture. Do not use verbose curl output. The example's fixed historical timestamp intentionally exercises the STALE warning; to test a fresh screen, set `collected_at` and `published_at` in a local copy to the current UTC time and set `stale` to false.
4. In the editor, inspect the full, horizontal half, vertical half and quadrant previews. Use **Force Refresh** to request screen generation, then inspect the plugin/virtual device playlist. A successful webhook does not guarantee an immediate new render; TRMNL queues rendering separately.
5. Confirm eight synthetic room cards in the full view (four columns, two rows), `—` for unknown readings, a valid `0`, and the stale banner. Each card emphasizes measured temperature, with target, humidity and an explicit heating/idle/unknown indicator below. Horizontal half uses a single row of four cards; vertical half uses a two-by-two grid. Half views show the first four selected rooms and quadrant shows the first two, with an explicit count when truncated. Change a local display name to text containing `<`/`&` and confirm it renders as text.
6. After visual approval, select the eight real rooms below and start `serve`. Look for the generic `TRMNL snapshot published` status and compare the screen with the cached JSON. Stop/restart with `TRMNL_ENABLED=false` to disable all sending.

Build an import ZIP locally without publishing it:

```sh
make trmnl-package
# Import bin/tado-bridge-trmnl.zip in TRMNL.
```

## Select the rooms explicitly

Use `discover` or the authenticated local snapshot to find home/zone IDs; that output is private. Stop `serve` before running a CLI command against the same credentials file. Make a local copy of `rooms.example.json` outside the repository, replace the placeholders with your eight home/zone pairs, and choose display names. Names default to `Room 1`, `Room 2`, etc. when omitted; original upstream names are never copied automatically.

```sh
export TRMNL_ROOMS="$(cat "$HOME/.config/tado-bridge/trmnl-rooms.json")"
export TRMNL_ENABLED=true
export TRMNL_INTERVAL=30m
# Set TRMNL_WEBHOOK_URL privately to the URL from your private plugin.
./bin/tado-bridge serve
```

The adapter accepts only official `https://trmnl.com/api/custom_plugins/<secret>` or `https://usetrmnl.com/api/custom_plugins/<secret>` URLs, with no query, fragment, userinfo, alternate port or redirects. Use the canonical URL shown by your plugin. It does not accept arbitrary/self-hosted destinations. No device or account API key is required. HTTP proxy environment variables are ignored by this client.

| Setting | Default | Behavior |
|---|---|---|
| `TRMNL_ENABLED` | `false` | Explicit opt-in; disabled ignores other adapter settings and sends nothing |
| `TRMNL_WEBHOOK_URL` | unset | Required when enabled; secret official HTTPS private-plugin webhook |
| `TRMNL_INTERVAL` | `30m` | Delay between normal checks; accepted range `5m`–`24h` |
| `TRMNL_ROOMS` | unset | Required JSON array selecting 1–16 distinct `{home_id,zone_id,name}` entries; display names at most 48 UTF-8 bytes |

Compose forwards the same settings. Its expanded configuration contains the webhook secret; validate with `docker compose config --quiet` and do not share full output. Existing credentials mounts, loopback publishing and API authentication remain unchanged.

## Delivery and freshness

The publisher reads the immutable cache only, never a tado client. It checks every five seconds until a first snapshot exists, then follows `TRMNL_INTERVAL`. Sends never overlap. A successful collection timestamp is sent once per process and sent once more when it becomes stale. Newer snapshots replace pending failed updates. Deduplication is in memory and resets on restart. Timeouts can occur after the server accepted a request, so exactly-once delivery is not guaranteed; replacement payloads avoid appending duplicate rows.

Every POST wraps the complete replacement object in `merge_variables`, containing fixed `source`, `collected_at`, `published_at`, `max_age_seconds`, `stale` and the selected `rooms`. `published_at` is the send-attempt time, not a remote acknowledgement. Optional readings remain null; missing selections produce unavailable rows with null readings and their chosen alias, never fall back to other rooms. `stale` uses `MAX_SNAPSHOT_AGE`; collection age does not prove sensor freshness. Templates also compare collection age with render time, so later renders can show stale status if the bridge stopped. An already displayed e-paper image cannot change until the device receives another render.

The full JSON request is limited to 5,000 bytes. Oversized payloads are rejected locally; reduce selected rooms or shorten display names. There is a ten-second HTTP timeout. Transient network/5xx failures retry after 30, 60 and 120 seconds plus up to 25% jitter, then return to the normal interval. `Retry-After` seconds/dates can extend waits; 429 without a usable deadline waits at least one hour. Other errors, including authentication/strategy/size errors and redirects, wait the normal interval. Logs show only generic outcomes and HTTP status, never destination URLs, response bodies or payloads. Failures do not stop collection or the local API. SIGTERM cancels an active request and joins the publisher before releasing credentials.

## Verified upstream contract

Checked against official sources on 2026-10-19; this adapter adds no Go dependencies. Go 1.27.2 remains the latest stable release in the [official download feed](https://go.dev/dl/?mode=json).

- [Webhook API](https://docs.trmnl.com/go/private-plugins/webhooks): POST `merge_variables`, success 200, baseline payload limit 5 KB, rate limit 12 requests/hour and render scheduling. Default publishing is below that limit; retries and other senders share the account's policy.
- [Import/export format](https://help.trmnl.com/en/articles/10542599-importing-and-exporting-private-plugins): flat ZIP with settings and the four viewport templates.
- [Private plugins](https://help.trmnl.com/en/articles/9510536-private-plugins) and [debugging](https://help.trmnl.com/en/articles/11586187-debugging-private-plugins): preview, Force Refresh and logs.
- [Framework table](https://trmnl.com/framework/docs/3.4/table) and [title bar](https://trmnl.com/framework/docs/3.4/title_bar): framework classes and layout siblings. Templates use ordinary Liquid with escaped aliases, no external assets, JavaScript or third-party requests.

The four templates were also rendered locally using official LiquidJS 10.30.0 (registry integrity verified), checking escaped aliases, null/zero values, room limits, empty cache and fresh/stale states. This validates Liquid output, not TRMNL pixel rendering.

Tests use a local HTTPS server and fake scheduling for payload contracts, privacy, disabled/empty cache behavior, deduplication, stale transition, retry deadlines, redirects, timeout and cancellation. Real plugin/device acceptance is a manual step; automated tests do not send to TRMNL.
