#!/usr/bin/env python3
"""Merge user-annotated JSON back into db.json.

Usage:
  python3 merge_annotations.py /path/to/annotations.json

The annotations.json format (exported by the standalone HTML annotator):
  {
    "captcha_id_1": {"drag": 150, "gap_x": 200},
    "captcha_id_2": {"drag": 80, "gap_x": 130},
    ...
  }
"""
import json
import os
import sys

SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
DB_FILE = os.path.join(SCRIPT_DIR, "captcha_db", "db.json")


def main():
    if len(sys.argv) < 2:
        print(f"Usage: {sys.argv[0]} <annotations.json>")
        return 1

    ann_path = sys.argv[1]
    if not os.path.exists(ann_path):
        print(f"ERROR: {ann_path} not found")
        return 1

    with open(ann_path) as f:
        annotations = json.load(f)

    print(f"Loaded {len(annotations)} annotations from {ann_path}")

    # Load existing DB
    if os.path.exists(DB_FILE):
        with open(DB_FILE) as f:
            db = json.load(f)
    else:
        db = {}

    # Merge
    merged = 0
    for captcha_id, ann in annotations.items():
        if captcha_id not in db:
            db[captcha_id] = {}
        # Always overwrite (latest annotation wins)
        db[captcha_id]["manual_drag_distance"] = int(ann["drag"])
        db[captcha_id]["manual_gap_x"] = int(ann["gap_x"])
        merged += 1
        print(f"  {captcha_id[:8]}: drag={ann['drag']}px, gap_x={ann['gap_x']}px")

    # Save
    os.makedirs(os.path.dirname(DB_FILE), exist_ok=True)
    with open(DB_FILE, "w") as f:
        json.dump(db, f, indent=2, ensure_ascii=False)

    print(f"\nMerged {merged} annotations into {DB_FILE}")
    print(f"Total manual annotations: {sum(1 for v in db.values() if v.get('manual_drag_distance') is not None)}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
