@echo off
rem mti-sip-phone Windows build script: native + cross builds, output to dist\
rem Depends: Go 1.23+, MinGW-W64 gcc (CGO; WinLibs or MSYS2)
rem NOTE: keep this file ASCII-only, cmd.exe parses bat as GBK on zh-CN Windows
rem windows/386 GUI build needs 32-bit MinGW: MSYS2 (pacman -S mingw-w64-i686-gcc)
rem or WinLibs i686 at %USERPROFILE%\tools\winlibs-i686; without it the script
rem falls back to a headless pure-Go build.
rem linux targets are HEADLESS (-tags "headless noaudio", pure Go): fyne OpenGL
rem bindings require CGO, GUI for Linux must be built on Linux (./build.sh).

rem Auto-detect WinLibs (installed via winget), fallback to gcc in PATH
set "GCC="
for /d %%d in ("%LOCALAPPDATA%\Microsoft\WinGet\Packages\BrechtSanders.WinLibs.POSIX.UCRT_*") do (
    if exist "%%d\mingw64\bin\gcc.exe" set "GCC=%%d\mingw64\bin\gcc.exe"
)
if defined GCC (
    echo Using gcc: %GCC%
    set "CC=%GCC%"
) else (
    echo Using gcc from PATH (MinGW/MSYS2 required)
)

set "GOTOOLCHAIN=local"
if not exist dist mkdir dist

rem 1) Windows amd64 GUI: no console, tray app, CGO+audio
echo [1/5] Building dist\sip-phone-windows-amd64.exe (GUI, audio)...
go build -ldflags "-s -w -H=windowsgui" -o dist\sip-phone-windows-amd64.exe .\cmd\sip-phone
if errorlevel 1 goto :err

rem 2) Windows console build (-headless server mode / command line debug)
echo [2/5] Building dist\sip-phone-console.exe (console, for headless)...
go build -ldflags "-s -w" -o dist\sip-phone-console.exe .\cmd\sip-phone
if errorlevel 1 goto :err

rem 3) Windows 386: GUI+audio if 32-bit MinGW exists, else headless fallback
set "GCC32="
if exist "C:\msys64\mingw32\bin\i686-w64-mingw32-gcc.exe" set "GCC32=C:\msys64\mingw32\bin\i686-w64-mingw32-gcc.exe"
if exist "%USERPROFILE%\tools\winlibs-i686\mingw32\bin\i686-w64-mingw32-gcc.exe" set "GCC32=%USERPROFILE%\tools\winlibs-i686\mingw32\bin\i686-w64-mingw32-gcc.exe"
rem NOTE: no parentheses or trailing "..." in echo lines inside if/else blocks,
rem cmd.exe mis-parses them as a command group and aborts with a syntax error.
if defined GCC32 (
    echo [3/5] Building dist\sip-phone-windows-386.exe - GUI tray, audio, static
    set CGO_ENABLED=1
    set CC=%GCC32%
    set GOOS=windows
    set GOARCH=386
    go build -ldflags "-s -w -H=windowsgui -extldflags -static" -o dist\sip-phone-windows-386.exe .\cmd\sip-phone
    if errorlevel 1 goto :err
) else (
    echo [3/5] i686 gcc not found, building dist\sip-phone-windows-386-headless.exe
    set CGO_ENABLED=0
    set GOOS=windows
    set GOARCH=386
    go build -tags "headless noaudio" -ldflags "-s -w" -o dist\sip-phone-windows-386-headless.exe .\cmd\sip-phone
    if errorlevel 1 goto :err
)

rem 4) Linux amd64 headless (pure Go)
echo [4/5] Building dist\sip-phone-linux-amd64-headless (pure Go)...
set CGO_ENABLED=0
set GOOS=linux
set GOARCH=amd64
go build -tags "headless noaudio" -ldflags "-s -w" -o dist\sip-phone-linux-amd64-headless .\cmd\sip-phone
if errorlevel 1 goto :err

rem 5) Linux arm64 headless (pure Go)
echo [5/5] Building dist\sip-phone-linux-arm64-headless (pure Go)...
set GOARCH=arm64
go build -tags "headless noaudio" -ldflags "-s -w" -o dist\sip-phone-linux-arm64-headless .\cmd\sip-phone
if errorlevel 1 goto :err

echo Build OK:
echo   dist\sip-phone-windows-amd64.exe          - GUI tray, audio
echo   dist\sip-phone-console.exe                - console, headless/debug
if defined GCC32 goto :sum_gui
echo   dist\sip-phone-windows-386-headless.exe   - pure Go, WS service only
goto :sum_linux
:sum_gui
echo   dist\sip-phone-windows-386.exe            - GUI tray, audio, static
:sum_linux
echo   dist\sip-phone-linux-amd64-headless       - pure Go, WS service only
echo   dist\sip-phone-linux-arm64-headless       - pure Go, WS service only
echo GUI builds for linux need building on Linux - see ./build.sh
exit /b 0

:err
echo Build FAILED
exit /b 1
