#!/usr/bin/env python3
"""Watchdog: monitor bridge logs, auto-restart samweb if stuck.

Runs on shan as a scheduled task. Checks every 60s:
- If a bridge log hasn't been updated in 5 minutes (300s), samweb is stuck
- Kill + restart samweb via RestartSamweb schtask

Usage on shan:
    python3 C:\samweb\scripts\watchdog.py
"""
import os, time, sys

BRIDGE_LOGS = [
    ("qq", os.path.expanduser(r"~\.samweb\logs\qq_bridge.log")),
    ("carterdong168", os.path.expanduser(r"~\.samweb\logs\carterdong168_bridge.log")),
    ("139", os.path.expanduser(r"~\.samweb\logs\139_bridge.log")),
]

STUCK_THRESHOLD = 300  # 5 minutes no log update = stuck
CHECK_INTERVAL = 60   # check every 60s
RESTART_COOLDOWN = 120  # don't restart more than once per 2 min

import subprocess

def get_log_mtime(path):
    try:
        return os.path.getmtime(path)
    except:
        return 0

def restart_samweb():
    print(f"[{time.strftime('%H:%M:%S')}] restarting samweb (stuck detected)")
    subprocess.run(["taskkill", "/F", "/IM", "samweb.exe", "/T"],
                   capture_output=True, timeout=15)
    time.sleep(3)
    subprocess.run(["schtasks", "/Run", "/TN", "RestartSamweb"],
                   capture_output=True, timeout=10)
    print(f"[{time.strftime('%H:%M:%S')}] restart triggered")

def main():
    last_restart = 0
    print(f"[{time.strftime('%H:%M:%S')}] watchdog started (threshold={STUCK_THRESHOLD}s)")
    while True:
        now = time.time()
        all_stuck = True
        any_stuck = False
        any_running = False
        
        for name, path in BRIDGE_LOGS:
            mtime = get_log_mtime(path)
            if mtime > 0:
                age = now - mtime
                if age < STUCK_THRESHOLD:
                    all_stuck = False
                    any_running = True
                else:
                    any_stuck = True
                    print(f"[{time.strftime('%H:%M:%S')}] {name}: STUCK ({int(age)}s since last log)")
                # Log status every 5 checks
                if int(now) % 300 == 0:
                    print(f"[{time.strftime('%H:%M:%S')}] {name}: last update {int(age)}s ago")
            # If log doesn't exist, bridge might not be running yet
        
        # Restart if ANY bridge is stuck AND we haven't restarted recently
        # (even one stuck bridge blocks the group message loop)
        if any_stuck and any_running and (now - last_restart) > RESTART_COOLDOWN:
            # Double-check: maybe all bridges just haven't started yet
            time.sleep(10)  # wait 10s and recheck
            now2 = time.time()
            still_stuck = True
            for name, path in BRIDGE_LOGS:
                mtime = get_log_mtime(path)
                if mtime > 0 and (now2 - mtime) < STUCK_THRESHOLD:
                    still_stuck = False
                    break
            if True:  # any_stuck is enough, restart immediately
                restart_samweb()
                last_restart = now2
        
        time.sleep(CHECK_INTERVAL)

if __name__ == "__main__":
    main()
