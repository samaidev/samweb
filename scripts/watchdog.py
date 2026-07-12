#!/usr/bin/env python3
"""Watchdog v2: restart samweb if bridges are stuck.

Checks for bridge activity by looking at recent log lines that indicate
actual processing (not just AICQ SDK INFO messages like WS reconnect).
"""
import os, time, subprocess

BRIDGE_LOGS = [
    ("qq", os.path.expanduser(r"~\.samweb\logs\qq_bridge.log")),
    ("carterdong168", os.path.expanduser(r"~\.samweb\logs\carterdong168_bridge.log")),
    ("139", os.path.expanduser(r"~\.samweb\logs\139_bridge.log")),
]

STUCK_THRESHOLD = 300  # 5 min
CHECK_INTERVAL = 60
RESTART_COOLDOWN = 120

# Lines that indicate ACTUAL bridge activity (not AICQ SDK noise)
ACTIVITY_KEYWORDS = [
    "group message", "processing group", "sending group",
    "group response", "group msg:", "send attempt",
    "chat input found", "streaming...", "response complete",
    "anti-freeze", "on_message", "on_group_message",
    "bridge ready", "session binding",
]

def get_last_activity_time(path):
    """Get timestamp of last meaningful log line (not AICQ SDK INFO)."""
    try:
        # Read last 50 lines
        with open(path, 'rb') as f:
            f.seek(0, 2)
            size = f.tell()
            f.seek(max(0, size - 5000))
            lines = f.read().decode('utf-8', 'replace').split('\n')
        for line in reversed(lines):
            if any(kw in line for kw in ACTIVITY_KEYWORDS):
                # Extract timestamp like [05:02:34] or [01:23:45]
                if '[' in line and ']' in line:
                    ts_str = line.split('[')[1].split(']')[0]
                    try:
                        h, m, s = map(int, ts_str.split(':'))
                        now = time.localtime()
                        # Assume today
                        ts_sec = h * 3600 + m * 60 + s
                        now_sec = now.tm_hour * 3600 + now.tm_min * 60 + now.tm_sec
                        age = now_sec - ts_sec
                        if age < 0:
                            age += 86400  # crossed midnight
                        return age
                    except:
                        pass
        return 9999  # no activity found = very old
    except:
        return 9999

def restart_samweb():
    print(f"[{time.strftime('%H:%M:%S')}] RESTARTING samweb (stuck detected)")
    subprocess.run(["taskkill", "/F", "/IM", "samweb.exe", "/T"],
                   capture_output=True, timeout=15)
    time.sleep(3)
    subprocess.run(["schtasks", "/Run", "/TN", "RestartSamweb"],
                   capture_output=True, timeout=10)

def main():
    last_restart = 0
    print(f"[{time.strftime('%H:%M:%S')}] watchdog v2 started (threshold={STUCK_THRESHOLD}s)")
    while True:
        now = time.time()
        any_stuck = False
        for name, path in BRIDGE_LOGS:
            age = get_last_activity_time(path)
            if age > STUCK_THRESHOLD:
                any_stuck = True
                print(f"[{time.strftime('%H:%M:%S')}] {name}: STUCK ({age}s since last activity)")
            else:
                print(f"[{time.strftime('%H:%M:%S')}] {name}: ok ({age}s)")

        if any_stuck and (now - last_restart) > RESTART_COOLDOWN:
            restart_samweb()
            last_restart = now

        time.sleep(CHECK_INTERVAL)

if __name__ == "__main__":
    main()
