# Backup

Everything lives in one SQLite file, `DATA_DIR/pocket-movement.db` (WAL mode — never copy it while the app runs; use one of these):

- `pocket-movement backup` (or `docker exec pocket-movement pocket-movement backup`) writes a consistent copy via `VACUUM INTO` to `DATA_DIR/backups/`.
- `GET /api/v1/backup` downloads the same thing (Settings → Backup & about, admins).

`DATA_DIR/imports/` only holds uploads until their import finishes.

Restore: stop the app, replace `pocket-movement.db` (remove any `-wal`/`-shm` next to it), start. Journeys, visits and day summaries are derived from the points: if in doubt, `POST /api/v1/rebuild` (Import & export → Rebuild) recomputes them; your confirmations, names, saved places and corrected modes are kept.

For a portable copy of the raw history, export GeoJSON (Import & export → Export, or `GET /api/v1/export?all=1`) — it imports back into Pocket Movement or Dawarich.
