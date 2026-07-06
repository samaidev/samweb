#!/usr/bin/env python3
"""Collect captcha images from a live page by repeatedly triggering
the captcha popup.

This connects to a running samweb agent and clicks the "点击开始验证"
button repeatedly, downloading each unique captcha bg + puzzle image.

Usage:
  python3 collect_captchas.py --base http://127.0.0.1:7777 --token YOUR_TOKEN

After collection, the images and metadata are saved to captcha_db/.

The script uses a perceptual hash (pHash) to detect duplicates — Aliyun's
AIGC captcha pool is finite (typically 10-30 unique images), so after
enough refreshes you'll start seeing the same images repeat. Once you
have all unique images, stop the script with Ctrl+C.
"""
import argparse
import base64
import hashlib
import io
import json
import os
import re
import sys
import time
import urllib.request

import numpy as np
from PIL import Image

# Default paths
SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
DB_DIR = os.path.join(SCRIPT_DIR, "captcha_db")
DB_FILE = os.path.join(DB_DIR, "db.json")


def phash(data, size=8):
    """Compute a simple perceptual hash of an image.

    Resize to size x size, convert to grayscale, threshold at mean.
    Returns a single integer with size*size bits.
    """
    img = Image.open(io.BytesIO(data)).convert("L").resize((size, size))
    arr = np.array(img)
    mean = arr.mean()
    bits = (arr > mean).flatten()
    h = 0
    for b in bits:
        h = (h << 1) | int(b)
    return h


def fetch_image(url, referer="https://chat.z.ai/"):
    """Download an image with proper headers."""
    req = urllib.request.Request(url, headers={
        "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
        "Referer": referer,
    })
    with urllib.request.urlopen(req, timeout=30) as resp:
        return resp.read()


def req(base, token, method, path, body=None, timeout=30):
    """Make a request to the samweb agent API."""
    url = base + path
    headers = {"Accept": "application/json"}
    if token:
        headers["Authorization"] = f"Bearer {token}"
    data = None
    if body is not None:
        data = json.dumps(body).encode()
        headers["Content-Type"] = "application/json"
    r = urllib.request.Request(url, data=data, method=method, headers=headers)
    with urllib.request.urlopen(r, timeout=timeout) as resp:
        payload = resp.read()
        ctype = resp.headers.get("Content-Type", "")
        if ctype.startswith("application/json"):
            return json.loads(payload)
        return payload


def eval_js(base, token, script):
    """Run a JS eval via the agent API."""
    r = req(base, token, "POST", "/agent/eval", body={"script": script})
    if isinstance(r, dict) and "value" in r:
        v = r["value"]
        if isinstance(v, dict) and "value" in v:
            v = v["value"]
        s = str(v).strip()
        # Unwrap nested quotes
        while len(s) >= 2 and s[0] == '"' and s[-1] == '"':
            s = s[1:-1].replace('\\"', '"').replace('\\\\', '\\')
        try:
            return json.loads(s)
        except Exception:
            return s
    return r


def cdp_mouse(base, token, etype, x, y, button="none", buttons=0, clickCount=0):
    """Send a CDP mouse event."""
    return req(base, token, "POST", "/agent/cdp-mouse", body={
        "type": etype, "x": x, "y": y,
        "button": button, "buttons": buttons, "clickCount": clickCount
    })


def get_captcha_dom(base, token):
    """Get the current captcha popup's image URLs and handle position."""
    script = r"""(function(){
        var bg = document.getElementById('aliyunCaptcha-img');
        var pz = document.getElementById('aliyunCaptcha-puzzle');
        var popup = document.getElementById('aliyunCaptcha-window-float');
        if (!bg || !pz) return JSON.stringify({error: 'captcha not visible'});
        var br = bg.getBoundingClientRect();
        var pr = pz.getBoundingClientRect();
        var pr2 = popup ? popup.getBoundingClientRect() : null;
        return JSON.stringify({
            popup_visible: popup ? (popup.style.display !== 'none' && pr2.width > 0) : false,
            bg_src: bg.src,
            pz_src: pz.src,
            bg_x: Math.round(br.left),
            bg_y: Math.round(br.top),
            bg_w: Math.round(br.width),
            bg_h: Math.round(br.height),
            pz_x: Math.round(pr.left),
            pz_y: Math.round(pr.top),
            pz_w: Math.round(pr.width),
            pz_h: Math.round(pr.height),
        });
    })()"""
    return eval_js(base, token, script)


def find_puzzle_content_bbox(puzzle_img):
    """Find the puzzle piece's content bounding box (alpha > 30)."""
    arr = np.array(puzzle_img.convert("RGBA"))
    alpha = arr[:, :, 3]
    cols = np.where(alpha.max(axis=0) > 30)[0]
    rows = np.where(alpha.max(axis=1) > 30)[0]
    if len(cols) == 0 or len(rows) == 0:
        return None
    return (int(cols[0]), int(rows[0]), int(cols[-1] + 1), int(rows[-1] + 1))


def extract_captcha_id(url):
    """Extract the captcha ID from an Aliyun captcha image URL.

    URLs look like:
      https://static-captcha-sgp.aliyuncs.com/qst/aigc/online/2/<uuid>/inpainted_with_mask.png
    """
    m = re.search(r'/online/\d+/([a-f0-9-]+)/', url)
    if m:
        return m.group(1)
    # Fallback: use the URL hash
    return hashlib.md5(url.encode()).hexdigest()[:16]


def collect(base, token, target_count=30, max_attempts=60, referer="https://chat.z.ai/"):
    """Collect unique captcha images.

    Args:
        base: samweb agent base URL
        token: agent auth token
        target_count: stop after collecting this many unique images
        max_attempts: max number of captcha refresh attempts
        referer: referer header for image downloads

    Returns:
        dict: the updated db.json
    """
    os.makedirs(DB_DIR, exist_ok=True)

    # Load existing DB
    if os.path.exists(DB_FILE):
        with open(DB_FILE) as f:
            db = json.load(f)
    else:
        db = {}
    print(f"Existing DB: {len(db)} entries")

    # Track pHashes we've already seen
    seen_hashes = {v.get("phash") for v in db.values() if v.get("phash")}
    collected = 0

    for attempt in range(max_attempts):
        # Click "点击开始验证" to show the captcha popup
        cdp_mouse(base, token, "mousePressed", 130, 421, "left", 1, 1)
        time.sleep(0.1)
        cdp_mouse(base, token, "mouseReleased", 130, 421, "left", 0, 1)
        time.sleep(2)

        info = get_captcha_dom(base, token)
        if not info or info.get("error") or not info.get("popup_visible"):
            print(f"[{attempt+1}] captcha not visible, refreshing...")
            # Try clicking the refresh button
            refresh_script = r"""(function(){
                var btn = document.getElementById('aliyunCaptcha-btn-refresh');
                if (!btn) return null;
                var r = btn.getBoundingClientRect();
                return JSON.stringify({x: Math.round(r.left + r.width/2), y: Math.round(r.top + r.height/2)});
            })()"""
            rinfo = eval_js(base, token, refresh_script)
            if rinfo and not isinstance(rinfo, str):
                cdp_mouse(base, token, "mousePressed", rinfo["x"], rinfo["y"], "left", 1, 1)
                time.sleep(0.1)
                cdp_mouse(base, token, "mouseReleased", rinfo["x"], rinfo["y"], "left", 0, 1)
                time.sleep(2)
            continue

        bg_url = info["bg_src"]
        pz_url = info["pz_src"]
        captcha_id = extract_captcha_id(bg_url)

        # Download both images
        try:
            bg_data = fetch_image(bg_url, referer)
            pz_data = fetch_image(pz_url, referer)
        except Exception as e:
            print(f"[{attempt+1}] download error: {e}")
            continue

        # Compute pHash of bg
        h = phash(bg_data)
        if h in seen_hashes:
            print(f"[{attempt+1}] duplicate (phash={h:016x}), refreshing...")
            # Refresh to get a different captcha
            refresh_script = r"""(function(){
                var btn = document.getElementById('aliyunCaptcha-btn-refresh');
                if (!btn) return null;
                var r = btn.getBoundingClientRect();
                return JSON.stringify({x: Math.round(r.left + r.width/2), y: Math.round(r.top + r.height/2)});
            })()"""
            rinfo = eval_js(base, token, refresh_script)
            if rinfo and not isinstance(rinfo, str):
                cdp_mouse(base, token, "mousePressed", rinfo["x"], rinfo["y"], "left", 1, 1)
                time.sleep(0.1)
                cdp_mouse(base, token, "mouseReleased", rinfo["x"], rinfo["y"], "left", 0, 1)
                time.sleep(2)
            continue

        seen_hashes.add(h)

        # Save images
        bg_path = os.path.join(DB_DIR, f"bg_{captcha_id}.png")
        pz_path = os.path.join(DB_DIR, f"pz_{captcha_id}.png")
        with open(bg_path, "wb") as f:
            f.write(bg_data)
        with open(pz_path, "wb") as f:
            f.write(pz_data)

        # Get puzzle content bbox
        pz_img = Image.open(io.BytesIO(pz_data))
        bbox = find_puzzle_content_bbox(pz_img)
        if bbox:
            pz_x0, pz_y0, pz_x1, pz_y1 = bbox
            handle_end_pz = pz_x1
        else:
            pz_x0 = pz_y0 = 0
            pz_x1 = pz_img.size[0]
            pz_y1 = pz_img.size[1]
            handle_end_pz = pz_img.size[0]

        # Update DB
        db[captcha_id] = {
            "phash": f"{h:016x}",
            "captcha_id": captcha_id,
            "bg_url": bg_url,
            "puzzle_url": pz_url,
            "bg_path": bg_path,
            "puzzle_path": pz_path,
            "puzzle_w": pz_img.size[0],
            "puzzle_h": pz_img.size[1],
            "pz_content_x0": pz_x0,
            "pz_content_y0": pz_y0,
            "pz_content_x1": pz_x1,
            "pz_content_y1": pz_y1,
            "handle_end_pz": handle_end_pz,
        }
        collected += 1
        print(f"[{attempt+1}] NEW captcha {captcha_id[:8]}, phash={h:016x}, "
              f"puzzle={pz_img.size[0]}x{pz_img.size[1]}, content=[{pz_x0},{pz_y0},{pz_x1},{pz_y1}]")

        # Save DB incrementally
        with open(DB_FILE, "w") as f:
            json.dump(db, f, indent=2, ensure_ascii=False)

        if collected >= target_count:
            print(f"\n>>> Collected {collected} unique captchas (target reached)")
            break

        # Refresh to get next captcha
        refresh_script = r"""(function(){
            var btn = document.getElementById('aliyunCaptcha-btn-refresh');
            if (!btn) return null;
            var r = btn.getBoundingClientRect();
            return JSON.stringify({x: Math.round(r.left + r.width/2), y: Math.round(r.top + r.height/2)});
        })()"""
        rinfo = eval_js(base, token, refresh_script)
        if rinfo and not isinstance(rinfo, str):
            cdp_mouse(base, token, "mousePressed", rinfo["x"], rinfo["y"], "left", 1, 1)
            time.sleep(0.1)
            cdp_mouse(base, token, "mouseReleased", rinfo["x"], rinfo["y"], "left", 0, 1)
            time.sleep(2)

    print(f"\n>>> Total unique captchas: {len(db)}")
    return db


def main():
    ap = argparse.ArgumentParser(description="Collect captcha images from a live page")
    ap.add_argument("--base", default="http://127.0.0.1:7777",
                    help="samweb agent base URL")
    ap.add_argument("--token", default="",
                    help="agent auth token")
    ap.add_argument("--count", type=int, default=30,
                    help="target number of unique captchas to collect")
    ap.add_argument("--max-attempts", type=int, default=60,
                    help="max refresh attempts")
    ap.add_argument("--referer", default="https://chat.z.ai/",
                    help="referer header for image downloads")
    args = ap.parse_args()

    # Health check
    print("Checking agent health...")
    r = req(args.base, args.token, "GET", "/agent/health")
    print(f"  status: {r.get('status')}")

    collect(args.base, args.token,
            target_count=args.count,
            max_attempts=args.max_attempts,
            referer=args.referer)


if __name__ == "__main__":
    sys.exit(main())
