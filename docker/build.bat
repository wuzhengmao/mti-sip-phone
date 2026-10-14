@echo off
rem Container cross-build entry for Windows (the actual logic lives in build.sh;
rem git-bash is required - bundled with Git for Windows).
rem
rem Usage:
rem   docker\build.bat               -> build all 10 artifacts into dist/ (run --libs first)
rem   docker\build.bat --libs        -> build third-party static libs only (video / Opus+AEC,
rem                                     slow, idempotent; runs once per machine)
rem   docker\build.bat --build-image -> build/verify the image only (first time or after
rem                                     Dockerfile changes)
rem   docker\build.bat --shell       -> interactive shell inside the container (troubleshooting)
rem   docker\build.bat --go 1.25.3   -> rebuild image with another Go version
rem
rem Prerequisites: Docker Desktop running, and this repo's drive allowed in
rem Docker Desktop - Settings - Resources - File sharing.
rem All arguments are passed through to build.sh; see docker/README.md and
rem build.sh for the details (Chinese docs live there).
rem
rem NOTE: comments here are English on purpose - cmd.exe parses .bat files with
rem the ANSI/GBK codepage and UTF-8 Chinese comment lines get mangled into
rem bogus command fragments.
setlocal

set "BASH="
if exist "%ProgramFiles%\Git\bin\bash.exe" set "BASH=%ProgramFiles%\Git\bin\bash.exe"
if not defined BASH if exist "%ProgramFiles(x86)%\Git\bin\bash.exe" set "BASH=%ProgramFiles(x86)%\Git\bin\bash.exe"
if not defined BASH if exist "%LOCALAPPDATA%\Programs\Git\bin\bash.exe" set "BASH=%LOCALAPPDATA%\Programs\Git\bin\bash.exe"
if not defined BASH for %%b in (bash.exe) do if not defined BASH set "BASH=%%~$PATH:b"
if not defined BASH goto :nobash

set "SCRIPT=%~dp0build.sh"
rem cmd does not treat \" as an escape; pass forward-slash path (bash accepts
rem D:/...) in single quotes - same form as scripts\release.bat.
set "SCRIPT=%SCRIPT:\=/%"
"%BASH%" -lc "exec '%SCRIPT%' %*"
if errorlevel 1 goto :err
exit /b 0

:nobash
echo bash.exe not found. Install Git for Windows (bundles git-bash) or put bash.exe on PATH.
exit /b 1

:err
echo Docker build failed. Check messages above.
exit /b 1
