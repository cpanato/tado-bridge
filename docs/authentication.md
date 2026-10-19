# M1 upstream verification

Sources checked on 2026-10-18 before implementing the client:

- [tado° device authorization guide](https://help.tado.com/en/articles/8565472-how-do-i-authenticate-to-access-the-rest-api): official OAuth endpoints, public client ID, offline scope, refresh rotation and `/me` example.
- [tado° REST usage limits](https://help.tado.com/en/articles/12165739-limitation-for-rest-api-usage): official quota explanation and `ratelimit` header format.
- [PyTado V3 client source](https://github.com/wmalgadey/PyTado/blob/master/PyTado/interface/api/my_tado.py) and [HTTP routing source](https://github.com/wmalgadey/PyTado/blob/master/PyTado/http.py): implementation evidence for the zones route and API base. This is a third-party client's primary source, not an official API contract.
- [PyTado X client source](https://github.com/wmalgadey/PyTado/blob/master/PyTado/interface/api/hops_tado.py) and [X response models](https://github.com/wmalgadey/PyTado/blob/master/PyTado/models/line_x/device.py): evidence for the X discovery route and `rooms`, `roomId`, `roomName` fields. These are third-party implementation sources; the route/schema were also checked with a live account, without persisting its responses.
- [X room-state models](https://github.com/wmalgadey/PyTado/blob/master/PyTado/models/line_x/room.py) and [V3 zone-state models](https://github.com/wmalgadey/PyTado/blob/master/PyTado/models/pre_line_x/zone.py): measurement fields for the one-shot `read` extension. X temperatures use `value`; V3 uses `celsius`. X heating power is top-level; V3 nests it in `activityDataPoints`.

## Implemented requests

| Method | Endpoint | Purpose |
|---|---|---|
| POST | `https://login.tado.com/oauth2/device_authorize` | Begin device login |
| POST | `https://login.tado.com/oauth2/token` | Poll device grant or refresh token |
| GET | `https://my.tado.com/api/v2/me` | Discover homes |
| GET | `https://my.tado.com/api/v2/homes/{homeID}` | Detect home generation (`LINE_X` selects X) |
| GET | `https://my.tado.com/api/v2/homes/{homeID}/zones` | Discover V3/V3+ zones |
| GET | `https://hops.tado.com/homes/{homeID}/roomsAndDevices` | Discover X rooms |
| GET | `https://hops.tado.com/homes/{homeID}/rooms` | Read all X room states in one request |
| GET | `https://my.tado.com/api/v2/homes/{homeID}/zones/{zoneID}/state` | Read one V3/V3+ zone state |

The public client ID is `1bb50063-6b0c-4d11-bd99-387f4a91cc46`, with `offline_access` to request refresh credentials. No client secret or password grant is used. OAuth parameters are form-encoded in the POST body to avoid placing credentials in URLs. The official examples use query parameters; the operator has successfully completed login with the form-body implementation.

The device grant is `urn:ietf:params:oauth:grant-type:device_code`; renewal uses `refresh_token`. Polling waits the returned interval (five seconds if absent), and adds five seconds after `slow_down`. Terminal denial, expiry, HTTP errors and transport failures stop the command. Redirects are disabled. HTTP requests have a 30-second timeout and honor cancellation.

For browser authorization, the CLI uses the returned `verification_uri_complete`, preserving provider parameters, or falls back to `verification_uri` if the complete URL is absent. Missing `client_id` and `user_code` query parameters are added. Verification URLs must use HTTPS on `login.tado.com`, without userinfo or fragments. Opening the bare URI caused a user-reported `missing_client_id` browser error; regression tests now cover both complete and fallback URLs. The operator subsequently completed login successfully.

The guide's prose and token example disagree about access-token duration; the CLI uses `expires_in` from the response. The guide describes refresh-token rotation and a maximum refresh lifetime of 30 days. Refresh errors do not delete the saved credentials or silently restart device login.

## Request accounting and quotas

Synthetic server observations from the tests:

- Two-home discovery (V3 only or mixed V3/X): **5 REST requests** (`/me`, then generation and zones/rooms per home).
- Device login with pending, slow-down and success: **4 OAuth requests** (one initiation and three polls).
- Successful renewal: **1 OAuth request**.
- One-shot REST failure, including 429: one attempted request at the failing step, without retry. `serve` adds the bounded retry policy described below.

The CLI prints REST and OAuth attempt counts separately. These are client-side HTTP attempts, not confirmed server-side quota debits; failed network attempts may not reach tado°. Normal successful discovery costs `1 + 2 * number of homes` REST calls and does not fetch zone states. The CLI never probes both generations' discovery routes for the same home. Legacy homes without a generation field use the V3 route, matching PyTado's generation detection convention.

The official quota article currently lists 100 requests/day, or 20,000/day with Auto-Assist/AI Assist. Treat 100/day as a conservative planning ceiling; actual account policy, shared use by other integrations, reset behavior, whether OAuth requests consume that quota and how limits are shared between the V3/X API hosts need live confirmation. A fixed 30-minute schedule with one X home would cost 144 REST requests/day before OAuth and other clients. M2 therefore increases the effective interval to fit the configured rolling budget, reserving refresh capacity as described below.

## Boundaries and remaining acceptance checks

Official documentation describes hobbyist/open-source access without technical support and says the REST API is not officially supported for third parties. No complete, supported V3 API schema or contractual availability guarantee was found in these sources. Nothing here implies a supported commercial API entitlement or an assessment of all applicable account terms.

Live investigation confirmed the operator's home reports `LINE_X`: the V3 zones route returned HTTP 200 with an empty list, while the X rooms-and-devices route returned HTTP 200 with eight rooms. M1 was explicitly expanded to support X cloud discovery. The domain remains generation-neutral; no device details are emitted. Local Matter remains out of scope.

The rebuilt CLI was then verified end to end: `discover` returned one home and eight zones, reporting `api=3 oauth=0`. Verification displayed counts only and did not save or log the account's home/room payloads.

The one-shot `read` extension was verified on the live X home: eight measured temperatures, eight humidity readings and eight explicit heating states, using three REST calls and one OAuth refresh. All eight rooms reported `setting.power=OFF`, so normalized targets were `null`. Refresh and persistence completed successfully; no credentials or private readings were logged or saved as fixtures. Counts were displayed only for this manual acceptance check.

The new command costs `1 + 2 * number of homes + number of V3 zones` REST calls; X states need no additional per-room requests. Unknown sensor fields stay null. The completion timestamp is not a sensor timestamp; upstream temperature timestamps are preserved in UTC when available. `heating_active` uses only valid explicit heating-power percentages, never target-versus-measured inference. OFF settings suppress the inactive target temperature.

V3/V3+ live discovery and measurements still need verification. Fixtures are synthetic and are not presented as captured production responses. Do not paste raw upstream bodies, user codes or credential files into issues or commits. Inspect quota headers privately if needed; the CLI does not currently expose them.

The M2 local service uses the same read/refresh paths, with a per-request guard counting REST, OAuth and failed network attempts. The guard applies only to `serve`; one-shot commands remain explicitly initiated reads. The process-local rolling 24-hour budget defaults to 90. Request pacing reserves at least one refresh per cycle even when the current access token is valid. Counts reset on process restart; neither this counter nor restarting can reset tado's account-wide quota. Exact quota sharing with other clients/API hosts remains account-dependent.

M2 live acceptance on loopback returned health/readiness 200, rejected an unauthenticated snapshot request with 401 and served eight temperatures to an authenticated request. Repeated snapshot reads kept the same collection timestamp without additional upstream calls. Total upstream attempts were three REST calls plus one OAuth refresh; SIGTERM returned exit code zero. The test displayed only status/count summaries and did not persist snapshots or token values.

The service serializes credential access for its full lifetime. On 429 it honors Retry-After and exhausted RateLimit reset deadlines, with a conservative one-hour fallback if no deadline is supplied. Transient retries count against the same budget. See [M2 architecture](architecture.md) for details. M3 provides [containers](containers.md) and [optional cached metrics](metrics.md). External integrations remain a later milestone.
