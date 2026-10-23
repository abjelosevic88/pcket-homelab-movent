-- How a link reaches Dawarich: "direct" (the address and key typed in) or
-- "core" (asked from Pocket Core on every pull; no key is kept here).
ALTER TABLE dawarich_links ADD COLUMN source TEXT NOT NULL DEFAULT 'direct';
