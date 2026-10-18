# M1 upstream verification

Sources checked on 2026-10-18 before implementing the client:

- [tado° device authorization guide](https://help.tado.com/en/articles/8565472-how-do-i-authenticate-to-access-the-rest-api): official OAuth endpoints, public client ID, offline scope, refresh rotation and `/me` example.
- [tado° REST usage limits](https://help.tado.com/en/articles/12165739-limitation-for-rest-api-usage): official quota explanation and `ratelimit` header format.
- [PyTado V3 client source](https://github.com/wmalgadey/PyTado/blob/master/PyTado/interface/api/my_tado.py) and [HTTP routing source](https://github.com/wmalgadey/PyTado/blob/master/PyTado/http.py): implementation evidence for the zones route and API base. This is a third-party client's primary source, not an official API contract.
- [PyTado X client source](https://github.com/wmalgadey/PyTado/blob/master/PyTado/interface/api/hops_tado.py) and [X response models](https://github.com/wmalgadey/PyTado/blob/master/PyTado/models/line_x/device.py): evidence for the X discovery route and `rooms`, `roomId`, `roomName` fields. These are third-party implementation sources; the route/schema were also checked with a live account, without persisting its responses.

## Implemented requests

| Method | Endpoint | Purpose |
|---|---|---|
| POST | `https://login.tado.com/oauth2/device_authorize` | Begin device login |
| POST | `https://login.tado.com/oauth2/token` | Poll device grant or refresh token |
| GET | `https://my.tado.com/api/v2/me` | Discover homes |
| GET | `https://my.tado.com/api/v2/homes/{homeID}` | Detect home generation (`LINE_X` selects X) |
| GET | `https://my.tado.com/api/v2/homes/{homeID}/zones` | Discover V3/V3+ zones |
| GET | `https://hops.tado.com/homes/{homeID}/roomsAndDevices` | Discover X rooms |

The public client ID is `1bb50063-6b0c-4d11-bd99-387f4a91cc46`, with `offline_access` to request refresh credentials. No client secret or password grant is used. OAuth parameters are form-encoded in the POST body to avoid placing credentials in URLs. The official examples use query parameters; the operator has successfully completed login with the form-body implementation.

The device grant is `urn:ietf:params:oauth:grant-type:device_code`; renewal uses `refresh_token`. Polling waits the returned interval (five seconds if absent), and adds five seconds after `slow_down`. Terminal denial, expiry, HTTP errors and transport failures stop the command. Redirects are disabled. HTTP requests have a 30-second timeout and honor cancellation.

For browser authorization, the CLI uses the returned `verification_uri_complete`, preserving provider parameters, or falls back to `verification_uri` if the complete URL is absent. Missing `client_id` and `user_code` query parameters are added. Verification URLs must use HTTPS on `login.tado.com`, without userinfo or fragments. Opening the bare URI caused a user-reported `missing_client_id` browser error; regression tests now cover both complete and fallback URLs. The operator subsequently completed login successfully.

The guide's prose and token example disagree about access-token duration; the CLI uses `expires_in` from the response. The guide describes refresh-token rotation and a maximum refresh lifetime of 30 days. Refresh errors do not delete the saved credentials or silently restart device login.

## Request accounting and quotas

Synthetic server observations from the tests:

- Two-home discovery (V3 only or mixed V3/X): **5 REST requests** (`/me`, then generation and zones/rooms per home).
- Device login with pending, slow-down and success: **4 OAuth requests** (one initiation and three polls).
- Successful renewal: **1 OAuth request**.
- REST failure, including 429: one attempted request at the failing step, without retry.

The CLI prints REST and OAuth attempt counts separately. These are client-side HTTP attempts, not confirmed server-side quota debits; failed network attempts may not reach tado°. Normal successful discovery costs `1 + 2 * number of homes` REST calls and does not fetch zone states. The CLI never probes both generations' discovery routes for the same home. Legacy homes without a generation field use the V3 route, matching PyTado's generation detection convention.

The official quota article currently lists 100 requests/day, or 20,000/day with Auto-Assist/AI Assist. Treat 100/day as a conservative planning ceiling; actual account policy, shared use by other integrations, reset behavior, whether OAuth requests consume that quota and how limits are shared between the V3/X API hosts need live confirmation. M1 has no background polling or daily-budget configuration. A 30-minute schedule with one home would cost 144 REST requests/day before other clients or additional reads; cadence and discovery reuse must be revisited in M2.

## Boundaries and remaining acceptance checks

Official documentation describes hobbyist/open-source access without technical support and says the REST API is not officially supported for third parties. No complete, supported V3 API schema or contractual availability guarantee was found in these sources. Nothing here implies a supported commercial API entitlement or an assessment of all applicable account terms.

Live investigation confirmed the operator's home reports `LINE_X`: the V3 zones route returned HTTP 200 with an empty list, while the X rooms-and-devices route returned HTTP 200 with eight rooms. M1 was explicitly expanded to support X cloud discovery. The domain remains generation-neutral; no device details are emitted. Local Matter remains out of scope.

The rebuilt CLI was then verified end to end: `discover` returned one home and eight zones, reporting `api=3 oauth=0`. Verification displayed counts only and did not save or log the account's home/room payloads.

V3/V3+ live discovery and refresh-token rotation after expiry still need verification. Fixtures are synthetic and are not presented as captured production responses. Do not paste raw upstream bodies, user codes or credential files into issues or commits. Inspect quota headers privately if needed; the CLI does not currently expose them.

No measurement fields or heating-state inference are implemented in M1. HTTP service, snapshots, polling, retry/budget policy and containers remain later milestones. CI was added separately at the operator's request.
