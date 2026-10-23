# API

Everything is under `/api/v1` and speaks JSON. There are three ways in:

| Who | How | What |
|---|---|---|
| A phone or tracker app | a **device key** (`pm_…`) from Devices, as `?api_key=`, `Authorization: Bearer`, `X-API-Key`, HTTP basic auth (the key as the password) or, for Traccar/OsmAnd, the device identifier | send points (`track` scope), read history (`read` scope) or both (`full`) |
| A script | a **personal API token** (`pt_…`, Settings → API tokens) as a bearer token | everything you can do in the web app |
| The web app | the session cookie | everything |

On a box with Pocket Core, Core's user tokens and app-link tokens (`movement:read`, `movement:points`) work too.

## Sending positions

### Your own app: `POST /api/v1/locations`

One object, an array, or `{"locations": [...]}`. Each item is flat or a GeoJSON Point feature.

```sh
curl -X POST https://movement.example.com/api/v1/locations \
  -H "Authorization: Bearer pm_…" -H "Content-Type: application/json" \
  -d '[{"lat": 43.8563, "lon": 18.4131, "timestamp": "2026-10-23T08:30:00Z", "accuracy": 12, "speed": 1.4, "battery": 87}]'
# → {"received": 1, "added": 1, "duplicates": 0, "rejected": 0}
```

| Field | Aliases | Notes |
|---|---|---|
| `lat` | `latitude` | required |
| `lon` | `lng`, `long`, `longitude` | required |
| `timestamp` | `ts`, `tst`, `time`, `recorded_at` | ISO 8601 / RFC 3339, or unix seconds or milliseconds; missing = now |
| `accuracy` | `acc`, `horizontal_accuracy` | metres |
| `altitude` | `alt`, `elevation` | metres |
| `speed` | `spd`; `vel` / `speed_kmh` in km/h | m/s |
| `course` | `bearing`, `heading`, `dir`, `cog` | degrees |
| `battery` | `batt`, `battery_level` | 0–100 (or 0–1) |
| `battery_state` | `charging` (bool) | `unplugged`, `charging`, `full` |
| `tracker_id` | `device_id`, `tid` | free text |
| `motion` | `activity` | e.g. `walking`, `driving` |

Re-sending the same point is harmless: a point is unique per person, instant and position. Thousands of points per request are fine. Points outside the Earth, at 0,0, before 1980 or more than a day in the future are dropped; so are points inside a privacy zone set to "drop".

### Tracker apps (unchanged clients)

| App | Endpoint | Format |
|---|---|---|
| Dawarich (iOS / Android) | `POST /api/v1/points` | `{"locations": [GeoJSON features]}` with Overland's properties; answers `{"data": [...]}` |
| Overland | `POST /api/v1/overland/batches` | Overland batches; answers `{"result": "ok"}` |
| OwnTracks (HTTP mode) | `POST /api/v1/owntracks/points` | OwnTracks JSON (`_type: location`, `tst`, `vel` in km/h, `bs`, `conn`, …); answers with your **friends** (people sharing with you) as OwnTracks cards and locations |
| Traccar Client, OsmAnd and GPS hardware using the OsmAnd protocol | `GET/POST /osmand` (also `/api/v1/osmand`, `/api/v1/traccar/points`) | `?id=&lat=&lon=&timestamp=&speed=<knots>&bearing=&altitude=&accuracy=&batt=`, or Traccar Client's JSON (`location.coords`, `battery`, `activity`) |
| GPSLogger (Android) | `GET/POST /gpslogger` | `?lat=%LAT&lon=%LON&time=%TIME&alt=%ALT&acc=%ACC&spd=%SPD&dir=%DIR&batt=%BATT&ischarging=%ISCHARGING&ser=%SER` |
| Home Assistant | `POST /api/v1/locations` | a `rest_command` (Devices shows a ready YAML) |

Devices shows each app's exact URL, a QR code (OwnTracks and Overland configure themselves from it) and the steps.

## The Dawarich API

So the Dawarich apps, Pocket Travel, Pocket Garage, Pocket Core's Dawarich connector and Home Assistant's Dawarich integration work unchanged, these follow Dawarich 1.16:

| | |
|---|---|
| `GET /api/v1/health` | `{"status":"ok"}` with `X-Dawarich-Response` and `X-Dawarich-Version` headers; no key needed |
| `GET /api/v1/users/me` | `{"user": {id, email, theme, created_at, updated_at, settings}, "features": {...}}` |
| `GET/PATCH /api/v1/settings`, `GET/PATCH /api/v1/settings/mobile` | settings; the app's tracking settings |
| `GET /api/v1/points` | `start_at`, `end_at`, `order` (desc), `page`, `per_page` (≤ 10000), `slim=true`, `include_anomalies`; `X-Current-Page`, `X-Total-Pages` headers |
| `DELETE /api/v1/points/:id`, `DELETE /api/v1/points/bulk_destroy` `{point_ids}` | |
| `GET /api/v1/points/tracked_months` | `[{"year": 2026, "months": ["Jan", …]}]` |
| `GET /api/v1/visits`, `GET/PUT/PATCH/DELETE /api/v1/visits/:id`, `POST /api/v1/visits`, `GET /api/v1/visits/:id/possible_places` | visits with `place {latitude, longitude, id, address}` |
| `GET/POST /api/v1/areas`, `DELETE /api/v1/areas/:id` | areas are saved places |
| `GET /api/v1/places` | |
| `GET /api/v1/stats` | `totalDistanceKm`, `yearlyStats[].monthlyDistanceKm`, … |
| `GET /api/v1/countries/visited_cities` | |
| `GET /api/v1/timeline?start_at&end_at` (≤ 31 days) | per day: `summary` and `entries` (journeys and visits) |
| `GET /api/v1/tracks` | journeys as GeoJSON LineStrings |
| `GET /api/v1/photos` | `[]` |

## The app's own API

Times are unix seconds unless named `*_at`. Dates are `YYYY-MM-DD` in the person's time zone. Distances are metres; speeds in journeys are km/h.

| | |
|---|---|
| `GET /bootstrap` | who you are, settings, devices, counts, first/last day |
| `GET /now` | the newest position, the visit or journey it belongs to, devices, today so far |
| `GET /live` | server-sent events: `point` for your own new positions and those of people sharing with you |
| `GET /map?from&to` | journeys (encoded polylines, re-simplified for long ranges), visits, the raw line of points not yet analysed, totals, bounds |
| `GET /map/points?from&to&limit&outliers=1` | rows `[id, lat, lng, ts, km/h, accuracy, altitude, outlier]`, evenly thinned to `limit` |
| `GET /map/heat?from&to&cell=` (or `all=1`) | `[lat, lng, count]` per cell — the heat map and the fog of war |
| `GET /day/:date` | the day's summary and entries in order, previous/next day with data |
| `GET /calendar?year=` or `?from&to` | day summaries |
| `GET /insights?year=` or `?from&to` | totals, months, modes, weekdays, countries, cities, top places, records, time split |
| `GET /countries`, `GET /countries/:code/shape` | every country with first/last day and years; outlines for the scratch map |
| `GET /summary?date=` or `?from&to` (≤ 92 days) | a compact description for assistants |
| `GET /where?at=2026-10-20T14:30` | the visit or journey at that time, and the nearest point |
| `GET /search?q=` | your places by name, address or city |
| `GET /geocode?q=` | forward geocoding |
| `POST /places`, `GET/PUT/DELETE /places/:id`, `POST /places/:id/merge {ids}` | saved places name and confirm visits inside them, also past ones |
| `POST /visits/bulk {ids, status}` | confirm or decline many |
| `GET /journeys/:id`, `PATCH /journeys/:id {mode}`, `GET /journeys/:id/points` | a hand-set mode survives every rebuild |
| `GET/POST /devices`, `GET/PUT/DELETE /devices/:id`, `POST /devices/:id/rotate`, `GET /devices/:id/setup`, `GET /devices/:id/qr.svg` | |
| `GET/POST /imports` (multipart `file`), `POST /imports/dawarich {url, api_key}`, `DELETE /imports/:id?points=1` | |
| `GET /export?format=geojson|gpx|csv&from&to|all=1&zones=1` | |
| `POST /rebuild`, `DELETE /history?from&to` | |
| `GET/POST/PUT/DELETE /shares`, `GET /family`, `GET /shared/:token` (public) | |
| `GET/POST/PUT/DELETE /privacy-zones` | |
| `GET/PUT /account/settings`, `GET /system`, users, tokens, `GET /backup` | |
