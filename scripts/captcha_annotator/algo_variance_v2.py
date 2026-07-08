#!/usr/bin/env python3
"""Algorithm v9: masked variance minimization.

Slide the puzzle piece's alpha mask across the bg at the puzzle's
y-range. For each position, compute the variance of bg pixels under
the mask. The gap (inpainted) has LOW variance because inpainting
produces smooth regions.

Returns the drag distance that aligns the puzzle piece with the gap.

This is the most reliable algorithm for Aliyun AIGC captchas.
"""
import os
import sys
import json

import numpy as np
from PIL import Image, ImageDraw

SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
DB_DIR = os.path.join(SCRIPT_DIR, "captcha_db")
DB_FILE = os.path.join(DB_DIR, "db.json")


def find_puzzle_content_bbox(puzzle_img, alpha_thresh=200):
    """Find the puzzle piece's content bounding box using strict alpha.

    Returns (bbox, mask) where bbox is (x0, y0, x1, y1) and mask is the
    cropped alpha mask (already cropped to bbox).
    """
    arr = np.array(puzzle_img.convert("RGBA"))
    alpha = arr[:, :, 3]
    cols = np.where(alpha.max(axis=0) > alpha_thresh)[0]
    rows = np.where(alpha.max(axis=1) > alpha_thresh)[0]
    if len(cols) == 0 or len(rows) == 0:
        return None, None
    bbox = (int(cols[0]), int(rows[0]), int(cols[-1] + 1), int(rows[-1] + 1))
    mask_full = (alpha > alpha_thresh).astype(np.float32)
    # Crop mask to bbox
    mask = mask_full[bbox[1]:bbox[3], bbox[0]:bbox[2]]
    return bbox, mask


def find_gap(bg_img, puzzle_img):
    """Find gap by masked variance minimization.

    Returns the drag distance (px).
    """
    pz_bbox, mask = find_puzzle_content_bbox(puzzle_img)
    if pz_bbox is None:
        return None
    pz_x0, pz_y0, pz_x1, pz_y1 = pz_bbox
    pz_h, pz_w = mask.shape

    bg_gray = np.array(bg_img.convert("L")).astype(np.float32)
    h_bg, w_bg = bg_gray.shape

    # Slide mask across bg, compute masked variance
    scores = []
    for x in range(max(0, w_bg // 6), w_bg - pz_w + 1):
        # Skip the puzzle's initial position
        if abs(x - pz_x0) < 10:
            continue
        bg_region = bg_gray[pz_y0:pz_y0 + pz_h, x:x + pz_w]
        if bg_region.shape != (pz_h, pz_w):
            continue
        # Masked variance
        n = mask.sum()
        if n == 0:
            continue
        mean = (bg_region * mask).sum() / n
        var = (((bg_region - mean) * mask) ** 2).sum() / n
        scores.append((x, float(var)))

    if not scores:
        return None

    # Lowest variance = gap
    scores.sort(key=lambda s: s[1])
    best_x = scores[0][0]
    return best_x - pz_x0


def main():
    with open(DB_FILE) as f:
        db = json.load(f)

    print(f"Testing masked-variance algorithm against {len(db)} captchas")
    print(f"{'captcha_id':<10} {'manual':<8} {'v9':<8} {'diff':<8}")
    print("-" * 40)

    diffs = []
    for captcha_id, info in db.items():
        bg_path = os.path.join(DB_DIR, f"bg_{captcha_id}.png")
        pz_path = os.path.join(DB_DIR, f"pz_{captcha_id}.png")
        if not os.path.exists(bg_path) or not os.path.exists(pz_path):
            continue
        bg_img = Image.open(bg_path)
        pz_img = Image.open(pz_path)
        manual = info.get('manual_drag_distance')
        if manual is None:
            continue

        pred = find_gap(bg_img, pz_img)
        if pred is None:
            print(f"{captcha_id[:8]:<10} {manual:<8} {'FAIL':<8}")
            continue
        diff = pred - manual
        print(f"{captcha_id[:8]:<10} {manual:<8} {pred:<8} {diff:+d}")
        diffs.append(abs(diff))

    if diffs:
        diffs_arr = np.array(diffs)
        print(f"\n{'='*40}")
        print(f"Mean |err|: {diffs_arr.mean():.1f}px")
        print(f"Max |err|: {diffs_arr.max():.1f}px")
        print(f"Hits (<5px): {(diffs_arr < 5).sum()}/{len(diffs)}")
        print(f"Hits (<10px): {(diffs_arr < 10).sum()}/{len(diffs)}")
        print(f"Hits (<15px): {(diffs_arr < 15).sum()}/{len(diffs)}")


if __name__ == "__main__":
    sys.exit(main())
