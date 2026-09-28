@echo off
rem mti-sip-phone Windows build script
rem Depends: Go 1.23+, MinGW-W64 gcc (CGO; WinLibs or MSYS2)
rem NOTE: keep this file ASCII-only, cmd.exe parses bat as GBK on zh-CN Windows

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

rem 1) Main binary: GUI subsystem (no console window, tray-only, REQ-UI-013)
echo [1/2] Building sip-phone.exe (GUI subsystem, no console)...
go build -ldflags "-s -w -H=windowsgui" -o bin\sip-phone.exe .\cmd\sip-phone
if errorlevel 1 goto :err

rem 2) Console binary for headless/debug use
echo [2/2] Building sip-phone-console.exe (console, for headless)...
go build -ldflags "-s -w" -o bin\sip-phone-console.exe .\cmd\sip-phone
if errorlevel 1 goto :err

echo Build OK: bin\sip-phone.exe (tray, no console), bin\sip-phone-console.exe (console)
exit /b 0

:err
echo Build FAILED
exit /b 1
