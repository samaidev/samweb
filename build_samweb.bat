@echo off
REM Compile-only script for SamWeb. Produces samweb.exe.new (does not restart).

cd /d C:\samweb
set GOOS=windows
set GOARCH=amd64
set CGO_ENABLED=1
echo Building samweb.exe.new ...
go build -tags "desktop,production" -o samweb.exe.new ./cmd/samweb
if errorlevel 1 (
    echo BUILD FAILED
    exit /b 1
)
echo Build OK: samweb.exe.new
dir samweb.exe.new
