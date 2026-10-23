#!/usr/bin/env python3
"""Build internal/geo/data/countries.json.gz from Natural Earth (public domain).

    curl -sSLO https://raw.githubusercontent.com/nvkelso/natural-earth-vector/master/geojson/ne_50m_admin_0_countries.geojson
    python3 scripts/countries.py ne_50m_admin_0_countries.geojson internal/geo/data/countries.json.gz

Each country: ISO code, name, continent, bounding box and polygons as flat
[lng, lat, lng, lat, ...] rings rounded to 4 decimals (~10 m).
"""
import gzip
import json
import sys


def code(p):
    for k in ("ISO_A2_EH", "ISO_A2", "WB_A2"):
        v = p.get(k)
        if v and v != "-99":
            return v
    return p.get("ADM0_A3", "")[:2]


# Disputed areas count as the country most travellers would say they were in.
MERGE = {"Somaliland": ("SO", "Somalia"), "Turkish Republic of Northern Cyprus": ("CY", "Cyprus"),
         "N. Cyprus": ("CY", "Cyprus"), "Siachen Glacier": ("IN", "India")}


def main(src, dst):
    out = []
    for f in json.load(open(src))["features"]:
        p, g = f["properties"], f["geometry"]
        polys = g["coordinates"] if g["type"] == "MultiPolygon" else [g["coordinates"]]
        rings_out, xs, ys = [], [], []
        for poly in polys:
            rings = []
            for ring in poly:
                flat = []
                for x, y in ring:
                    flat += [round(x, 4), round(y, 4)]
                    xs.append(x)
                    ys.append(y)
                rings.append(flat)
            rings_out.append(rings)
        c, n = code(p), p.get("NAME_EN") or p["NAME"]
        c, n = MERGE.get(n, MERGE.get(p["NAME"], (c, n)))
        out.append({"c": c, "n": n, "k": p.get("CONTINENT", ""),
                    "b": [round(min(xs), 4), round(min(ys), 4), round(max(xs), 4), round(max(ys), 4)], "p": rings_out})
    with gzip.open(dst, "wt", compresslevel=9) as fh:
        json.dump(out, fh, separators=(",", ":"))


if __name__ == "__main__":
    main(sys.argv[1], sys.argv[2])
