# Configuration

Everything is an environment variable with a sensible default; preferences (units, time zone, map tiles, detection thresholds, notifications) are per person under Settings.

| Variable | Default | |
|---|---|---|
| `PORT` / `HOST` | `8080` / `0.0.0.0` | |
| `BASE_URL` | – | The public address (`https://movement.example.com`). Used in setup instructions, share links and sign-in with Pocket Core |
| `DATA_DIR` | `./data` | The database (`pocket-movement.db`) and uploads waiting to be imported (`imports/`) |
| `DB_PATH`, `IMPORTS_DIR` | under `DATA_DIR` | |
| `MAX_UPLOAD_BYTES` | 2 GiB | Largest import file |
| `TZ` | UTC | The server's time zone: day boundaries for anyone who has not picked their own |
| `AUTH_MODE` | `local` | `local` (accounts with passwords and/or Pocket Core sign-in), `proxy` (trust `AUTH_PROXY_HEADER` / `AUTH_PROXY_EMAIL_HEADER`), `none` (single user, no login — only behind something else) |
| `SECURE_COOKIES` | `false` | `true` behind HTTPS |
| `SESSION_TTL` | `720h` | |
| `ADMIN_EMAIL` / `ADMIN_PASSWORD` / `ADMIN_NAME` | – | Create the first admin on an empty database (otherwise the first visitor creates it) |
| `ADMIN_API_KEY` | – | Also give the first admin a device key with this value (full scope), so Pocket Travel/Garage or a Dawarich client can be pointed here before anyone signs in. At least 16 characters |
| `OIDC_ISSUER` / `OIDC_CLIENT_ID` / `OIDC_CLIENT_SECRET` / `OIDC_NAME` / `OIDC_AUTO_CREATE` | – | Sign in with any OpenID provider; with Pocket Core none are needed (Core issues a client at registration) |
| `GEOCODER` | `nominatim` | Names places: `nominatim`, `photon` or `off`. The public Nominatim is asked at most once a second, only for new places, answers cached on a ~25 m grid |
| `GEOCODER_URL` | public server | Your own Nominatim or Photon |
| `USER_AGENT` | `PocketMovement/1.0 (+…)` | Sent to the geocoder (Nominatim's policy wants a real one) |
| `REBUILD_DELAY` | `20s` | New points settle this long before journeys and visits are rebuilt |
| `CORE_URL` / `CORE_REGISTRATION_KEY` / `CORE_APP_URL` | – | [Pocket Core](https://github.com/abjelosevic88/pocket-homelab-core): register on start, sign in with Core, accept Core tokens and app links, publish events. `CORE_APP_URL` is how Core reaches this app (defaults to `BASE_URL`) |
| `DEMO_DATA` | `false` | Fill an empty box with two weeks of a made-up life in Sarajevo |
| `LOG_LEVEL` / `LOG_FORMAT` | `info` / `text` | `debug` shows every request and rebuild |
| `METRICS_ENABLED` | `true` | Prometheus text at `/metrics` |
| `TRUST_PROXY` | `true` | Use `X-Forwarded-For` for client addresses |

## Detection settings (per person)

| Setting | Default | |
|---|---|---|
| Visit radius | 100 m | How far you may wander and still be "at" a place |
| Minimum stay | 5 min | Shorter stops are part of the journey |
| Journey gap | 30 min | A silence longer than this while moving ends a journey (phone off, tunnel) |
| Maximum accuracy | 150 m | Less accurate points are kept but left out of routes and totals (0 keeps all) |
| Maximum speed | 400 km/h | A jump faster than this that comes straight back is a GPS spike; a long one-way jump (a flight with the phone off) is not |

Changing any of them, or your time zone, rebuilds your whole history in the background, month by month. Confirmed and declined visits, your names, saved places and corrected transport modes are kept.

## Pocket Core

With `CORE_URL`, Movement registers as `movement`, offers `movement:read` (timeline, summaries, reports) and `movement:points` (raw points, the Dawarich API) to other Pocket apps through app links, uses Pocket Health (`health:read`, the Workouts page; or `HEALTH_URL`-less direct URL + token under Settings → Connected apps), and publishes:

| Topic | When | Payload (never coordinates) |
|---|---|---|
| `movement.visit_detected` | a new stay was found in the last few hours | `place`, `started_at`, `ended_at`, `duration_minutes`, `status`, `url` |
| `movement.country_entered` | the newest position is in another country (Settings → Notifications) | `country`, `country_name`, `from_country`, `at`, `url` |
| `movement.tracking_stopped` | no position for N hours (Settings → Notifications) | `last_point_at`, `silent_hours`, `url` |
| `movement.tracking_resumed` | positions arrive again | `url` |

Every payload carries `user_email`.
