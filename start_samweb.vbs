Set WshShell = CreateObject("WScript.Shell")
WshShell.Environment("Process").Item("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS") = "--remote-debugging-port=9222 --remote-allow-origins=*"
WshShell.Run "cmd /c C:\samweb\samweb.exe > C:\samweb\run.log 2>&1", 0, False
