#!/usr/bin/env python3
"""Test multiple gap-detection algorithms against the 15 manually
annotated captchas to find the most accurate one.

Algorithms tested:
  - v7: handle edge matching (current)
  - v5_canny: Canny edges + cv2.matchTemplate (chxj1992 approach)
  - v_color: color matching of puzzle content against bg
  - v_color_handle: color matching of just the handle (narrow part)
"""
import os
import sys
import json
import io

import numpy as np
import cv2
from PIL import Image, ImageDraw
from scipy import ndimage

SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, SCRIPT_DIR)

DB_DIR = os.path.join(SCRIPT_DIR, "captcha_db")
DB_FILE = os.path.join(DB_DIR, "db.json")


def find_puzzle_content_bbox(puzzle_img):
    arr = np.array(puzzle_img.convert("RGBA"))
    alpha = arr[:, :, 3]
    cols = np.where(alpha.max(axis=0) > 30)[0]
    rows = np.where(alpha.max(axis=1) > 30)[0]
    if len(cols) == 0 or len(rows) == 0:
        return None
    return (int(cols[0]), int(rows[0]), int(cols[-1] + 1), int(rows[-1] + 1))


def find_handle_region(puzzle_img, narrow_threshold=35, min_narrow_run=15):
    arr = np.array(puzzle_img.convert("RGBA"))
    alpha = arr[:, :, 3]
    cols = np.where(alpha.max(axis=0) > 30)[0]
    if len(cols) == 0:
        return None
    pz_x0, pz_x1 = cols[0], cols[-1] + 1
    col_counts = np.array([(alpha[:, x] > 30).sum() for x in range(pz_x0, pz_x1)])
    handle_start = None
    handle_end = None
    best_run_start = None
    best_run_len = 0
    cur_run_start = None
    cur_run_len = 0
    for i, c in enumerate(col_counts):
        x = pz_x0 + i
        if c < narrow_threshold:
            if cur_run_start is None:
                cur_run_start = x
            cur_run_len += 1
        else:
            if cur_run_len > best_run_len:
                best_run_len = cur_run_len
                best_run_start = cur_run_start
            cur_run_start = None
            cur_run_len = 0
    if cur_run_len > best_run_len:
        best_run_len = cur_run_len
        best_run_start = cur_run_start
    if best_run_start is None or best_run_len < min_narrow_run:
        return None
    return best_run_start, best_run_start + best_run_len - 1


# --- Algorithm 1: v7 (handle edge matching) ---
def algo_v7(bg_img, puzzle_img):
    handle_region = find_handle_region(puzzle_img)
    if not handle_region:
        return None
    handle_start, handle_end = handle_region
    handle_w = handle_end - handle_start + 1

    pz_bbox = find_puzzle_content_bbox(puzzle_img)
    if not pz_bbox:
        return None
    pz_x0, pz_y0, pz_x1, pz_y1 = pz_bbox

    bg_gray = np.array(bg_img.convert("L"))
    h_bg, w_bg = bg_gray.shape
    sx = np.abs(ndimage.sobel(bg_gray, axis=1))
    col_grad = sx.sum(axis=0)
    kernel = np.ones(5) / 5
    cs = np.convolve(col_grad, kernel, mode='same')

    exclude_x0, exclude_x1 = pz_x0, pz_x1
    best_score = -1
    best_x = None
    for x in range(max(0, w_bg // 6), w_bg - handle_w + 1):
        if exclude_x0 - 5 <= x <= exclude_x1 + 5:
            continue
        left_score = cs[x]
        right_score = cs[x + handle_w - 1]
        score = left_score + right_score
        if score > best_score:
            best_score = score
            best_x = x
    if best_x is None:
        return None
    # Drag distance = handle_right_x_in_bg - handle_end_pz
    handle_right_x_in_bg = best_x + handle_w - 1
    return handle_right_x_in_bg - handle_end


# --- Algorithm 2: Canny + matchTemplate (chxj1992) ---
def algo_canny_match(bg_img, puzzle_img):
    pz_bbox = find_puzzle_content_bbox(puzzle_img)
    if not pz_bbox:
        return None
    pz_x0, pz_y0, pz_x1, pz_y1 = pz_bbox

    # Crop puzzle to content
    pz_arr = np.array(puzzle_img.convert("RGBA"))
    pz_crop = pz_arr[pz_y0:pz_y1, pz_x0:pz_x1, :3]
    pz_alpha = pz_arr[pz_y0:pz_y1, pz_x0:pz_x1, 3]

    # Canny on both
    bg_gray = cv2.cvtColor(np.array(bg_img.convert("RGB")), cv2.COLOR_RGB2GRAY)
    bg_blur = cv2.GaussianBlur(bg_gray, (3, 3), 0)
    bg_canny = cv2.Canny(bg_blur, 100, 200)

    pz_gray = cv2.cvtColor(pz_crop, cv2.COLOR_RGB2GRAY)
    pz_blur = cv2.GaussianBlur(pz_gray, (3, 3), 0)
    pz_canny = cv2.Canny(pz_blur, 100, 200)

    # Apply puzzle alpha mask to its Canny edges
    mask = (pz_alpha > 30).astype(np.uint8) * 255
    pz_masked = cv2.bitwise_and(pz_canny, pz_canny, mask=mask)

    # matchTemplate
    result = cv2.matchTemplate(bg_canny, pz_masked, cv2.TM_CCOEFF_NORMED)
    # Get top candidates
    # Exclude the puzzle's initial position
    h_bg, w_bg = result.shape
    candidates = []
    for y in range(h_bg):
        for x in range(w_bg):
            # Exclude initial position (puzzle starts at x=pz_x0 in bg)
            if abs(x - pz_x0) < 10:
                continue
            # Only consider y near pz_y0
            if abs(y - pz_y0) > 10:
                continue
            candidates.append((x, y, result[y, x]))
    if not candidates:
        return None
    candidates.sort(key=lambda c: -c[2])
    best_x = candidates[0][0]
    # Drag distance = best_x - pz_x0
    return best_x - pz_x0


# --- Algorithm 3: Color matching (puzzle content vs bg) ---
def algo_color_match(bg_img, puzzle_img):
    pz_bbox = find_puzzle_content_bbox(puzzle_img)
    if not pz_bbox:
        return None
    pz_x0, pz_y0, pz_x1, pz_y1 = pz_bbox

    pz_arr = np.array(puzzle_img.convert("RGBA")).astype(np.int32)
    pz_rgb = pz_arr[pz_y0:pz_y1, pz_x0:pz_x1, :3]
    pz_alpha = pz_arr[pz_y0:pz_y1, pz_x0:pz_x1, 3]
    mask = (pz_alpha > 30).astype(np.int32)

    bg_arr = np.array(bg_img.convert("RGB")).astype(np.int32)
    h_bg, w_bg, _ = bg_arr.shape
    pz_h, pz_w = pz_rgb.shape[:2]

    # Exclude the puzzle's initial position
    scores = []
    for x in range(max(0, w_bg // 6), w_bg - pz_w + 1):
        if pz_x0 - 5 <= x <= pz_x1 + 5:
            continue
        bg_region = bg_arr[pz_y0:pz_y0 + pz_h, x:x + pz_w, :]
        if bg_region.shape != pz_rgb.shape:
            continue
        diff = np.abs(bg_region - pz_rgb)
        masked_diff = diff * mask[:, :, None]
        if mask.sum() > 0:
            score = masked_diff.sum() / (mask.sum() * 3)
            scores.append((x, float(score)))
    if not scores:
        return None
    scores.sort(key=lambda s: s[1])
    best_x = scores[0][0]
    return best_x - pz_x0


# --- Algorithm 4: Color matching of handle only ---
def algo_color_match_handle(bg_img, puzzle_img):
    handle_region = find_handle_region(puzzle_img)
    if not handle_region:
        return None
    handle_start, handle_end = handle_region
    handle_w = handle_end - handle_start + 1

    pz_bbox = find_puzzle_content_bbox(puzzle_img)
    if not pz_bbox:
        return None
    pz_x0, pz_y0, pz_x1, pz_y1 = pz_bbox
    pz_h = pz_y1 - pz_y0

    pz_arr = np.array(puzzle_img.convert("RGBA")).astype(np.int32)
    handle_rgb = pz_arr[pz_y0:pz_y1, handle_start:handle_end + 1, :3]
    handle_alpha = pz_arr[pz_y0:pz_y1, handle_start:handle_end + 1, 3]
    mask = (handle_alpha > 30).astype(np.int32)

    bg_arr = np.array(bg_img.convert("RGB")).astype(np.int32)
    h_bg, w_bg, _ = bg_arr.shape

    scores = []
    for x in range(max(0, w_bg // 6), w_bg - handle_w + 1):
        if pz_x0 - 5 <= x <= pz_x1 + 5:
            continue
        bg_region = bg_arr[pz_y0:pz_y0 + pz_h, x:x + handle_w, :]
        if bg_region.shape != handle_rgb.shape:
            continue
        diff = np.abs(bg_region - handle_rgb)
        masked_diff = diff * mask[:, :, None]
        if mask.sum() > 0:
            score = masked_diff.sum() / (mask.sum() * 3)
            scores.append((x, float(score)))
    if not scores:
        return None
    scores.sort(key=lambda s: s[1])
    best_x = scores[0][0]
    # Drag = handle_right_x_in_bg - handle_end_pz
    return (best_x + handle_w - 1) - handle_end


def main():
    with open(DB_FILE) as f:
        db = json.load(f)

    print(f"Testing 4 algorithms against {len(db)} annotated captchas")
    print(f"{'captcha_id':<10} {'manual':<8} {'v7':<8} {'canny':<8} {'color':<8} {'color_h':<8}")
    print("-" * 60)

    algos = {
        'v7': algo_v7,
        'canny': algo_canny_match,
        'color': algo_color_match,
        'color_h': algo_color_match_handle,
    }

    results = {name: [] for name in algos}
    manual_vals = []

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

        manual_vals.append(manual)
        row = f"{captcha_id[:8]:<10} {manual:<8}"
        for name, algo in algos.items():
            try:
                pred = algo(bg_img, pz_img)
                if pred is None:
                    row += f"{'FAIL':<8}"
                    results[name].append(None)
                else:
                    row += f"{pred:<8}"
                    results[name].append(pred)
            except Exception as e:
                row += f"{'ERR':<8}"
                results[name].append(None)
        print(row)

    # Compute stats
    print("\n" + "=" * 60)
    print(f"{'Algorithm':<15} {'Mean |err|':<15} {'Max |err|':<15} {'Hits (<5px)':<15}")
    print("-" * 60)
    manual_arr = np.array(manual_vals)
    for name, preds in results.items():
        preds_clean = [(m, p) for m, p in zip(manual_vals, preds) if p is not None]
        if not preds_clean:
            print(f"{name:<15} {'N/A':<15} {'N/A':<15} {'N/A':<15}")
            continue
        m_arr = np.array([x[0] for x in preds_clean])
        p_arr = np.array([x[1] for x in preds_clean])
        errs = np.abs(m_arr - p_arr)
        mean_err = errs.mean()
        max_err = errs.max()
        hits = (errs < 5).sum()
        print(f"{name:<15} {mean_err:<15.1f} {max_err:<15.1f} {hits}/{len(errs):<15}")


if __name__ == "__main__":
    sys.exit(main())
