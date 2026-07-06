# Captcha Annotator

A self-contained, browser-based annotation tool for slider-puzzle captchas
(Aliyun AIGC, Geetest, etc.). Designed to bootstrap a **captcha image
fingerprint → gap position database** that can be used for fully automated
login flows.

## Why this exists

Aliyun's AIGC slider-puzzle captcha (used by z.ai, modelscope, and many
others) generates captchas from a **finite pool of pre-rendered images**.
After enough samples are collected (typically 10-30 unique images), the
same images start repeating. Once we have a database of `(image_hash →
gap_position)`, we can solve any future captcha by:

1. Compute pHash of the incoming bg image
2. Look up the gap position in our DB
3. Drag the slider to that position via CDP trusted events

No image recognition needed at runtime — just a hash lookup.

## Files

```
captcha_annotator/
├── README.md                          # this file
├── collect_captchas.py                # scrape captcha images from a live page
├── build_standalone_html.py           # build self-contained HTML annotator
├── merge_annotations.py               # merge user annotations into db.json
├── query_db.py                        # lookup gap position by image hash
├── captcha_db/
│   ├── db.json                        # the fingerprint → gap position DB
│   ├── bg_<captcha_id>.png            # bg images (collected)
│   ├── pz_<captcha_id>.png            # puzzle piece images (collected)
│   └── ...
└── templates/
    └── annotator.html                 # HTML template (gets data embedded)
```

## Usage

### Step 1: Collect captcha images

```python
# Edit the agent URL and credentials in collect_captchas.py first
python3 collect_captchas.py --base http://127.0.0.1:7777 --token YOUR_TOKEN
```

This will:
1. Connect to the running samweb agent
2. Click "点击开始验证" repeatedly to trigger captcha popups
3. Download each unique bg + puzzle image
4. Compute pHash for each, skip duplicates
5. Save to `captcha_db/bg_<id>.png` and `captcha_db/pz_<id>.png`
6. Update `captcha_db/db.json` with image metadata

### Step 2: Build the standalone HTML annotator

```python
python3 build_standalone_html.py
```

This produces `annotate_standalone.html` (a single self-contained file
with all images embedded as base64). Open it in any browser — no server
needed.

### Step 3: Annotate

1. Open `annotate_standalone.html` in Chrome/Edge/Firefox
2. For each captcha:
   - Drag the puzzle piece (semi-transparent overlay) to align with the gap
   - Click **✓ 确认位置**
   - Click **下一张 ▶**
3. After all images are annotated, click **📦 导出标注结果**
4. Save the downloaded `annotations.json`

### Step 4: Merge annotations into the DB

```python
python3 merge_annotations.py /path/to/annotations.json
```

This merges the user-annotated drag distances into `captcha_db/db.json`
under the `manual_drag_distance` and `manual_gap_x` fields.

### Step 5: Use the DB in your solver

```python
from query_db import lookup_gap

# When you encounter a captcha:
gap = lookup_gap(bg_image_path)
if gap:
    drag_distance = gap['manual_drag_distance']
    # ... drag the slider to this position via CDP ...
```

## DB Schema (`db.json`)

```json
{
  "<captcha_id>": {
    "phash": "ffffc1c3c1c18181",
    "captcha_id": "<uuid>",
    "bg_url": "https://static-captcha-sgp.aliyuncs.com/...",
    "puzzle_url": "https://static-captcha-sgp.aliyuncs.com/...",
    "bg_path": "captcha_db/bg_<id>.png",
    "puzzle_path": "captcha_db/pz_<id>.png",
    "puzzle_w": 91,
    "puzzle_h": 300,
    "pz_content_x0": 0,
    "pz_content_y0": 217,
    "pz_content_x1": 91,
    "pz_content_y1": 288,
    "handle_end_pz": 91,
    "v7_drag": 220,
    "manual_drag_distance": 215,
    "manual_gap_x": 306
  },
  ...
}
```

## Key fields

- `phash`: 8x8 perceptual hash of the bg image (for matching)
- `manual_drag_distance`: **the ground-truth** drag distance (px) that
  the user manually verified aligns the puzzle piece with the gap
- `manual_gap_x`: the gap's left edge x-coordinate in bg image coords
  (= manual_drag_distance + handle_end_pz)
- `handle_end_pz`: the puzzle piece's right content edge in puzzle-image
  coords (used to compute drag distance from gap_x)
- `v7_drag`: the algorithm-predicted drag distance (for reference/fallback)

## Supported captcha types

Currently supports **Aliyun AIGC slider-puzzle** (the type used by z.ai
and modelscope.cn). The framework is extensible — to add support for
other captcha types (Geetest, Tencent, etc.), modify the image-collection
logic in `collect_captchas.py`.

## Requirements

- Python 3.10+
- Pillow, numpy, scipy (for image processing)
- A running samweb agent with CDP enabled (for collecting images)
- A modern browser (for annotation)
