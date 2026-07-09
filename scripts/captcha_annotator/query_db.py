#!/usr/bin/env python3
"""Query the captcha DB by image hash.

Given a captcha bg image (as a file path or raw bytes), compute its
pHash and look it up in db.json. If found, return the manual_drag_distance
(the ground-truth drag distance to align the puzzle piece with the gap).

Usage:
  # As a library
  from query_db import lookup_gap
  gap = lookup_gap(bg_image_path)
  if gap:
      drag_distance = gap['manual_drag_distance']

  # As a CLI
  python3 query_db.py /path/to/bg_image.png
"""
import io
import json
import os
import sys

import numpy as np
from PIL import Image

SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
DB_FILE = os.path.join(SCRIPT_DIR, "captcha_db", "db.json")


def phash(data, size=8):
    """Compute pHash of image data (bytes or PIL Image)."""
    if isinstance(data, (bytes, bytearray)):
        img = Image.open(io.BytesIO(data)).convert("L").resize((size, size))
    else:
        img = data.convert("L").resize((size, size))
    arr = np.array(img)
    mean = arr.mean()
    bits = (arr > mean).flatten()
    h = 0
    for b in bits:
        h = (h << 1) | int(b)
    return h


def hamming_distance(h1, h2):
    """Bit-level Hamming distance between two hashes."""
    return bin(h1 ^ h2).count("1")


def load_db():
    if not os.path.exists(DB_FILE):
        return {}
    with open(DB_FILE) as f:
        return json.load(f)


def lookup_gap(bg_image, max_distance=5):
    """Look up the gap position for a bg image.

    Args:
        bg_image: either a file path (str), raw bytes, or PIL Image
        max_distance: max acceptable Hamming distance for a match
            (lower = stricter; 0 = exact match; 5 = allow some variation)

    Returns:
        dict with keys: captcha_id, manual_drag_distance, manual_gap_x,
        handle_end_pz, etc. Or None if no match found.
    """
    db = load_db()
    if not db:
        return None

    # Compute pHash of the input image
    if isinstance(bg_image, str):
        with open(bg_image, "rb") as f:
            data = f.read()
        h = phash(data)
    elif isinstance(bg_image, (bytes, bytearray)):
        h = phash(bg_image)
    elif isinstance(bg_image, Image.Image):
        h = phash(bg_image)
    else:
        raise ValueError("bg_image must be a path, bytes, or PIL Image")

    # Find the closest match
    best_match = None
    best_distance = float("inf")
    for captcha_id, info in db.items():
        stored_hash = info.get("phash")
        if not stored_hash:
            continue
        try:
            stored_h = int(stored_hash, 16)
        except (ValueError, TypeError):
            continue
        dist = hamming_distance(h, stored_h)
        if dist < best_distance:
            best_distance = dist
            best_match = captcha_id

    if best_match is None or best_distance > max_distance:
        return None

    info = db[best_match]
    return {
        "captcha_id": best_match,
        "phash_distance": best_distance,
        "manual_drag_distance": info.get("manual_drag_distance"),
        "manual_gap_x": info.get("manual_gap_x"),
        "handle_end_pz": info.get("handle_end_pz"),
        "v7_drag": info.get("v7_drag"),
        "puzzle_w": info.get("puzzle_w"),
        "puzzle_h": info.get("puzzle_h"),
    }


def main():
    """CLI mode: python3 query_db.py /path/to/bg_image.png"""
    if len(sys.argv) < 2:
        print(f"Usage: {sys.argv[0]} <bg_image_path>")
        return 1

    bg_path = sys.argv[1]
    if not os.path.exists(bg_path):
        print(f"ERROR: {bg_path} not found")
        return 1

    result = lookup_gap(bg_path)
    if result:
        print(f"✓ MATCH (distance={result['phash_distance']})")
        print(f"  captcha_id: {result['captcha_id']}")
        print(f"  manual_drag_distance: {result['manual_drag_distance']}px")
        print(f"  manual_gap_x: {result['manual_gap_x']}px")
        print(f"  handle_end_pz: {result['handle_end_pz']}")
        return 0
    else:
        print(f"✗ No match found (max distance exceeded)")
        return 1


if __name__ == "__main__":
    sys.exit(main())
