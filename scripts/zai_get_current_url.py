#!/usr/bin/env python3
"""Query samweb's /agent/state to find out what URL is currently loaded.

This is the answer to: "登录之后 samweb 没有把当前 URL 告诉我".
The state endpoint returns {url, title, tabs, activeTab, canBack, canForward}.
"""
import json
import os
import sys

sys.path.insert(0, "/home/z/my-project/scripts")
from shan_lib.agent import Agent


def main():
    verbose = "-v" in sys.argv
    a = Agent(verbose=verbose)
    try:
        st = a.state()
        print("=== samweb /agent/state ===")
        print(json.dumps(st, ensure_ascii=False, indent=2))
        print()
        print("=== 当前页面 ===")
        print(f"URL:   {st.get('url','(空)')}")
        print(f"Title: {st.get('title','(空)')}")
        tabs = st.get("tabs", [])
        if tabs:
            print(f"\n=== 全部标签页（共 {len(tabs)} 个，active={st.get('activeTab')}）===")
            for t in tabs:
                mark = "*" if t.get("id") == st.get("activeTab") else " "
                print(f" {mark} [{t.get('id')}] {t.get('title','')}")
                print(f"     {t.get('url','')}")
    finally:
        a.close()


if __name__ == "__main__":
    main()
