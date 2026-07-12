@echo off
REM One-click start for SamWeb on shan.
REM Builds (if needed) then launches samweb.exe with CDP debugging enabled.

cd /d C:\samweb

REM Build if samweb.exe is missing or source is newer
if not exist samweb.exe goto :build
for /f "delims=" %%i in ('dir /b /s /o-d *.go 2^>nul ^| findstr /v "go-webview2-patch" ^| head -1') do set NEWEST_GO=%%i
if defined NEWEST_GO (
    xcopy /d /y /q "%NEWEST_GO%" "%TEMP%\samweb_marker" >nul 2>&1
    if errorlevel 1 goto :build
)

:run
REM Launch via wscript (hidden window, sets WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS)
wscript.exe "C:\samweb\start_samweb.vbs"
echo SamWeb started. Check run.log for output.
goto :eof

:build
echo Building samweb.exe ...
set GOOS=windows
set GOARCH=amd64
set CGO_ENABLED=1
go build -tags "desktop,production" -o samweb.exe.new ./cmd/samweb
if errorlevel 1 (
    echo BUILD FAILED
    exit /b 1
)
move /y samweb.exe.new samweb.exe
echo Build OK, starting ...
goto :run
