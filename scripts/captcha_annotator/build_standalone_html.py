#!/usr/bin/env python3
"""Build a self-contained HTML annotator file with all captcha images
embedded as base64.

The output is a single .html file that can be opened in any browser
without a server. The user drags each puzzle piece to align with the
gap, clicks "确认", and exports the annotations as JSON.

Usage:
  python3 build_standalone_html.py [--output annotate.html]
"""
import argparse
import base64
import io
import json
import os
import sys

from PIL import Image
import numpy as np

SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
DB_DIR = os.path.join(SCRIPT_DIR, "captcha_db")
DB_FILE = os.path.join(DB_DIR, "db.json")
TEMPLATE_PATH = os.path.join(SCRIPT_DIR, "templates", "annotator.html")
DEFAULT_OUTPUT = os.path.join(SCRIPT_DIR, "annotate_standalone.html")


def load_db():
    if not os.path.exists(DB_FILE):
        return {}
    with open(DB_FILE) as f:
        return json.load(f)


def img_to_data_url(path):
    with open(path, "rb") as f:
        data = f.read()
    b64 = base64.b64encode(data).decode()
    return f"data:image/png;base64,{b64}"


def build_manifest(db):
    """Build the manifest list with embedded base64 images."""
    manifest = []
    for captcha_id, info in db.items():
        bg_path = info.get("bg_path") or os.path.join(DB_DIR, f"bg_{captcha_id}.png")
        pz_path = info.get("puzzle_path") or os.path.join(DB_DIR, f"pz_{captcha_id}.png")
        if not os.path.exists(bg_path) or not os.path.exists(pz_path):
            print(f"  skipping {captcha_id[:8]}: missing image files")
            continue
        manifest.append({
            "id": captcha_id,
            "bg_b64": img_to_data_url(bg_path),
            "pz_b64": img_to_data_url(pz_path),
            "existing_drag": info.get("manual_drag_distance"),
            "v7_drag": info.get("v7_drag"),
            "handle_end_pz": info.get("handle_end_pz", 0),
            "puzzle_w": info.get("puzzle_w"),
            "puzzle_h": info.get("puzzle_h"),
        })
    return manifest


HTML_TEMPLATE = """<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<title>Captcha Annotator</title>
<style>
  body { font-family: -apple-system, sans-serif; background: #1a1a1a; color: #eee; margin: 0; padding: 20px; }
  h1 { color: #4CAF50; margin-top: 0; }
  .container { max-width: 800px; margin: 0 auto; }
  .captcha-card { background: #2a2a2a; border-radius: 8px; padding: 20px; margin-bottom: 20px; }
  .canvas-container { position: relative; width: 300px; height: 300px; margin: 0 auto; background: #000; border-radius: 4px; overflow: hidden; }
  .bg-img { position: absolute; top: 0; left: 0; user-select: none; -webkit-user-drag: none; }
  .puzzle-img { position: absolute; top: 0; left: 0; user-select: none; -webkit-user-drag: none; cursor: ew-resize; }
  .info { margin-top: 10px; font-size: 13px; color: #aaa; }
  .controls { margin-top: 15px; display: flex; gap: 10px; justify-content: center; flex-wrap: wrap; }
  button { padding: 8px 16px; border: none; border-radius: 4px; cursor: pointer; font-size: 14px; }
  .btn-confirm { background: #4CAF50; color: white; }
  .btn-skip { background: #666; color: white; }
  .btn-prev { background: #555; color: white; }
  .btn-next { background: #555; color: white; }
  .btn-export { background: #2196F3; color: white; }
  .drag-value { font-family: monospace; color: #4CAF50; font-weight: bold; }
  .status { margin-top: 10px; text-align: center; min-height: 20px; }
  .progress { background: #333; border-radius: 4px; padding: 4px 10px; margin-bottom: 10px; font-size: 13px; }
  .nav { display: flex; gap: 10px; justify-content: center; margin-bottom: 20px; }
  .help { background: #2d2d2d; padding: 10px; border-radius: 4px; margin-top: 10px; font-size: 12px; color: #aaa; }
</style>
</head>
<body>
<div class="container">
  <h1>Captcha Annotator</h1>
  <p>拖动拼图块（半透明形状）对齐缺口，点"确认"。</p>
  <div class="progress" id="progress">加载中...</div>

  <div class="captcha-card" id="currentCard">
    <div class="canvas-container" id="canvasContainer">
      <img class="bg-img" id="bgImg" width="300" height="300" draggable="false">
      <img class="puzzle-img" id="puzzleImg" draggable="false">
    </div>
    <div class="info" id="info"></div>
    <div class="controls">
      <button class="btn-prev" onclick="prev()">◀ 上一张</button>
      <button class="btn-confirm" onclick="confirm()">✓ 确认位置</button>
      <button class="btn-skip" onclick="skip()">跳过</button>
      <button class="btn-next" onclick="next()">下一张 ▶</button>
    </div>
    <div class="status" id="status"></div>
    <div class="help">
      💡 操作说明：<br>
      • 鼠标按住拼图块左右拖动<br>
      • 对齐到背景图上被涂改的缺口位置<br>
      • 拼图块的 left 值会被记录为 manual_drag_distance
    </div>
  </div>

  <div class="nav">
    <button class="btn-export" onclick="exportData()">📦 导出标注结果</button>
  </div>
</div>

<script>
// Embedded captcha data
const EMBEDDED_DATA = __EMBEDDED_DATA__;

let captchas = EMBEDDED_DATA;
let currentIdx = 0;
let annotations = {};
let dragState = { dragging: false, startX: 0, startOffset: 0 };

function showCurrent() {
  if (currentIdx >= captchas.length) {
    document.getElementById('progress').innerHTML = '✓ 全部完成！共标注 ' + Object.keys(annotations).length + ' 张';
    document.getElementById('currentCard').style.display = 'none';
    return;
  }
  const c = captchas[currentIdx];
  document.getElementById('bgImg').src = c.bg_b64;
  const pzImg = document.getElementById('puzzleImg');
  pzImg.src = c.pz_b64;
  pzImg.removeAttribute('width');
  pzImg.removeAttribute('height');

  let drag = 0;
  if (annotations[c.id]) {
    drag = annotations[c.id].drag;
  } else if (c.existing_drag !== null && c.existing_drag !== undefined) {
    drag = c.existing_drag;
    annotations[c.id] = { drag: drag, gap_x: drag + (c.handle_end_pz || 0) };
  }
  pzImg.style.left = drag + 'px';
  pzImg.style.top = '0px';

  const annotatedCount = Object.keys(annotations).length;
  document.getElementById('progress').textContent = '进度: ' + annotatedCount + '/' + captchas.length + ' 已标注 | 当前: ' + (currentIdx+1) + '/' + captchas.length;
  document.getElementById('info').innerHTML =
    '<div>Captcha ID: <code>' + c.id.slice(0, 16) + '...</code></div>' +
    '<div>图片尺寸: bg=300x300 puzzle=' + (c.puzzle_w||'?') + 'x' + (c.puzzle_h||'?') + '</div>' +
    '<div>v7_drag 参考: <span class="drag-value">' + (c.v7_drag !== null && c.v7_drag !== undefined ? c.v7_drag + 'px' : '?') + '</span> | handle_end_pz: ' + c.handle_end_pz + '</div>' +
    '<div>当前拖动: <span class="drag-value" id="dragValue">' + drag + 'px</span></div>';
  document.getElementById('status').textContent = '';
}

const puzzleImg = document.getElementById('puzzleImg');
puzzleImg.addEventListener('mousedown', (e) => {
  dragState.dragging = true;
  dragState.startX = e.clientX;
  dragState.startOffset = parseInt(puzzleImg.style.left) || 0;
  e.preventDefault();
});
document.addEventListener('mousemove', (e) => {
  if (!dragState.dragging) return;
  const dx = e.clientX - dragState.startX;
  let newOffset = dragState.startOffset + dx;
  newOffset = Math.max(-150, Math.min(300, newOffset));
  puzzleImg.style.left = newOffset + 'px';
  const dv = document.getElementById('dragValue');
  if (dv) dv.textContent = newOffset + 'px';
});
document.addEventListener('mouseup', () => { dragState.dragging = false; });

puzzleImg.addEventListener('touchstart', (e) => {
  dragState.dragging = true;
  dragState.startX = e.touches[0].clientX;
  dragState.startOffset = parseInt(puzzleImg.style.left) || 0;
  e.preventDefault();
});
document.addEventListener('touchmove', (e) => {
  if (!dragState.dragging) return;
  const dx = e.touches[0].clientX - dragState.startX;
  let newOffset = dragState.startOffset + dx;
  newOffset = Math.max(-150, Math.min(300, newOffset));
  puzzleImg.style.left = newOffset + 'px';
  const dv = document.getElementById('dragValue');
  if (dv) dv.textContent = newOffset + 'px';
  e.preventDefault();
}, { passive: false });
document.addEventListener('touchend', () => { dragState.dragging = false; });

function confirm() {
  if (currentIdx >= captchas.length) return;
  const c = captchas[currentIdx];
  const drag = parseInt(document.getElementById('puzzleImg').style.left) || 0;
  const gap_x = drag + (c.handle_end_pz || 0);
  annotations[c.id] = { drag: drag, gap_x: gap_x };
  document.getElementById('status').textContent = '✓ 已记录: drag=' + drag + 'px, gap_x=' + gap_x + 'px';
  const annotatedCount = Object.keys(annotations).length;
  document.getElementById('progress').textContent = '进度: ' + annotatedCount + '/' + captchas.length + ' 已标注 | 当前: ' + (currentIdx+1) + '/' + captchas.length;
  setTimeout(() => next(), 600);
}

function skip() { next(); }
function prev() { if (currentIdx > 0) { currentIdx--; showCurrent(); } }
function next() { currentIdx++; showCurrent(); }

function exportData() {
  const data = JSON.stringify(annotations, null, 2);
  const blob = new Blob([data], { type: 'application/json' });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = 'annotations.json';
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  URL.revokeObjectURL(url);
  const status = document.getElementById('status');
  status.innerHTML = '✓ <b>已下载 annotations.json！共 ' + Object.keys(annotations).length + ' 张标注</b><br>请把文件上传给 AI，告诉"标注完成"';
  status.style.color = '#4CAF50';
  status.style.fontSize = '16px';
}

showCurrent();
</script>
</body>
</html>
"""


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--output", default=DEFAULT_OUTPUT,
                    help="output HTML file path")
    args = ap.parse_args()

    db = load_db()
    print(f"Loaded DB: {len(db)} captchas")

    manifest = build_manifest(db)
    print(f"Built manifest: {len(manifest)} entries")

    # Embed manifest as JSON
    html = HTML_TEMPLATE.replace("__EMBEDDED_DATA__", json.dumps(manifest))

    with open(args.output, "w", encoding="utf-8") as f:
        f.write(html)
    size = os.path.getsize(args.output)
    print(f"Saved: {args.output}")
    print(f"Size: {size} bytes ({size/1024/1024:.1f} MB)")


if __name__ == "__main__":
    sys.exit(main())
