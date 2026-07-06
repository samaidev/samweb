#!/usr/bin/env python3
"""Recompute pHash for all captcha bg images and update db.json.

Run this after collecting new images, or after manually copying images
into captcha_db/. The pHash is needed for query_db.py to match incoming
captcha images against the DB.

Usage:
  python3 recompute_phash.py
"""
import io
import json
import os
import sys

import numpy as np
from PIL import Image

SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
DB_DIR = os.path.join(SCRIPT_DIR, "captcha_db")
DB_FILE = os.path.join(DB_DIR, "db.json")


def phash(img_path, size=8):
    """Compute pHash of an image file."""
    img = Image.open(img_path).convert("L").resize((size, size))
    arr = np.array(img)
    mean = arr.mean()
    bits = (arr > mean).flatten()
    h = 0
    for b in bits:
        h = (h << 1) | int(b)
    return f"{h:016x}"


def find_puzzle_content_bbox(puzzle_img):
    """Find the puzzle piece's content bounding box (alpha > 30)."""
    arr = np.array(puzzle_img.convert("RGBA"))
    alpha = arr[:, :, 3]
    cols = np.where(alpha.max(axis=0) > 30)[0]
    rows = np.where(alpha.max(axis=1) > 30)[0]
    if len(cols) == 0 or len(rows) == 0:
        return None
    return (int(cols[0]), int(rows[0]), int(cols[-1] + 1), int(rows[-1] + 1))


def main():
    if not os.path.exists(DB_FILE):
        print(f"ERROR: {DB_FILE} not found")
        return 1

    with open(DB_FILE) as f:
        db = json.load(f)
    print(f"Loaded DB: {len(db)} entries")

    updated = 0
    for captcha_id, info in db.items():
        bg_path = info.get("bg_path") or os.path.join(DB_DIR, f"bg_{captcha_id}.png")
        pz_path = info.get("puzzle_path") or os.path.join(DB_DIR, f"pz_{captcha_id}.png")

        if not os.path.exists(bg_path):
            print(f"  {captcha_id[:8]}: bg image missing, skipping")
            continue

        # Compute pHash
        h = phash(bg_path)
        info["phash"] = h

        # Also re-derive puzzle content bbox if missing
        if os.path.exists(pz_path) and not info.get("handle_end_pz"):
            pz_img = Image.open(pz_path)
            bbox = find_puzzle_content_bbox(pz_img)
            if bbox:
                info["pz_content_x0"] = bbox[0]
                info["pz_content_y0"] = bbox[1]
                info["pz_content_x1"] = bbox[2]
                info["pz_content_y1"] = bbox[3]
                info["handle_end_pz"] = bbox[2]
                info["puzzle_w"] = pz_img.size[0]
                info["puzzle_h"] = pz_img.size[1]

        # Ensure captcha_id field is set
        info["captcha_id"] = captcha_id
        info["bg_path"] = bg_path
        info["puzzle_path"] = pz_path

        updated += 1
        print(f"  {captcha_id[:8]}: phash={h}")

    with open(DB_FILE, "w") as f:
        json.dump(db, f, indent=2, ensure_ascii=False)

    print(f"\nUpdated {updated} entries in {DB_FILE}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
