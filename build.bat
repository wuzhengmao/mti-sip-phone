@echo off
rem mti-sip-phone Windows build script: native builds, output to dist\
rem Depends: Go 1.23+, MinGW-W64 gcc (CGO; WinLibs or MSYS2)
rem NOTE: keep this file ASCII-only, cmd.exe parses bat as GBK on zh-CN Windows
rem windows/386 GUI build needs 32-bit MinGW: MSYS2 (pacman -S mingw-w64-i686-gcc)
rem or WinLibs i686 at %USERPROFILE%\tools\winlibs-i686; without it the 386
rem target is skipped, there is no headless fallback anymore.
rem Linux GUI and Windows GUI cross builds from Linux are done by ./build.sh

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

rem Build timestamp (ISO 8601 local time) injected into core.BuildTime, shown in
rem the settings page version row. Empty when PowerShell is unavailable, then the
rem app falls back to the executable's modification time.
set "BUILD_TS="
for /f "usebackq delims=" %%t in (`powershell -NoProfile -Command "Get-Date -Format s"`) do set "BUILD_TS=%%t"
if defined BUILD_TS set "BUILD_TS=%BUILD_TS:~0,19%"
set "LDFLAGS=-s -w"
if defined BUILD_TS set "LDFLAGS=%LDFLAGS% -X mti-sip-phone/internal/core.BuildTime=%BUILD_TS%"

rem 1) Windows amd64 GUI: no console, tray app, CGO+audio
call :codec_setup windows-amd64
echo [1/3] Building dist\sip-phone-windows-amd64.exe (%FEATURES%)...
go build %TAGS_ARG% -ldflags "%LDFLAGS% -H=windowsgui" -o dist\sip-phone-windows-amd64.exe .\cmd\sip-phone
if errorlevel 1 goto :err

rem 2) Windows console build (-headless server mode / command line debug)
call :codec_setup windows-amd64
echo [2/3] Building dist\sip-phone-console.exe (%FEATURES%, console/headless)...
go build %TAGS_ARG% -ldflags "%LDFLAGS%" -o dist\sip-phone-console.exe .\cmd\sip-phone
if errorlevel 1 goto :err

rem 3) Windows 386 GUI: needs 32-bit MinGW, otherwise skipped
set "GCC32="
if exist "C:\msys64\mingw32\bin\i686-w64-mingw32-gcc.exe" set "GCC32=C:\msys64\mingw32\bin\i686-w64-mingw32-gcc.exe"
if exist "%USERPROFILE%\tools\winlibs-i686\mingw32\bin\i686-w64-mingw32-gcc.exe" set "GCC32=%USERPROFILE%\tools\winlibs-i686\mingw32\bin\i686-w64-mingw32-gcc.exe"
rem NOTE: no parentheses or trailing "..." in echo lines inside if/else blocks,
rem cmd.exe mis-parses them as a command group and aborts with a syntax error.
if not defined GCC32 goto :skip386
set CGO_ENABLED=1
set CC=%GCC32%
set GOOS=windows
set GOARCH=386
call :codec_setup windows-386
echo [3/3] Building dist\sip-phone-windows-386.exe (%FEATURES%, static)...
go build %TAGS_ARG% -ldflags "%LDFLAGS% -H=windowsgui -extldflags -static" -o dist\sip-phone-windows-386.exe .\cmd\sip-phone
if errorlevel 1 goto :err
goto :summary

:skip386
echo [3/3] i686 gcc not found, skipping dist\sip-phone-windows-386.exe
echo       install MSYS2 mingw-w64-i686-gcc or WinLibs i686 to enable it

:summary
echo Build OK:
echo   dist\sip-phone-windows-amd64.exe          - tray GUI, no console
echo   dist\sip-phone-console.exe                - console, headless/debug
if not defined GCC32 goto :done
echo   dist\sip-phone-windows-386.exe            - tray GUI, static
:done
echo Linux GUI and Windows GUI cross builds: run ./build.sh on Linux
exit /b 0

:err
echo Build FAILED
exit /b 1

rem ---------------------------------------------------------------
rem Codec static libraries, built once by the matching script:
rem   third_party\video\<target>  <- scripts\build-video-libs.bat  -> -tags video
rem   third_party\audio\<target>  <- scripts\build-audio-lib.bat   -> -tags audio
rem Both are independent: a missing one only disables that feature
rem (build without video / without Opus, which just drops it from SDP).
rem The go compiler reads the include/lib paths from the Go environment,
rem so CGO_CFLAGS/CGO_LDFLAGS are set here.
rem
rem Must check every .a the linker needs, not just the lib directory:
rem an aborted/failed libs build leaves an empty shell behind, and
rem treating it as usable fails the go build with a missing header
rem (fatal error: wels/codec_api.h -- seen in practice, same story for
rem  opus/opus.h).
rem Keep in sync with the #cgo LDFLAGS in internal\video\ffmpeg.go and
rem internal\media\codec_opus.go.
rem ---------------------------------------------------------------
:codec_setup
set "TAGS_ARG="
set "BUILD_TAGS="
set "VIDEO_ON="
set "AUDIO_ON="
set "FEATURES="
set "CGO_CFLAGS="
set "CGO_LDFLAGS="
call :video_setup %~1
call :audio_setup %~1
if defined BUILD_TAGS set "TAGS_ARG=-tags %BUILD_TAGS%"
rem FEATURES: what this target really gets (no codec lists - audio is the
rem base and Opus is part of audio; video and Opus come from their static
rem libraries). All four combinations spelled out; how to add a missing one
rem is in the hint lines above.
set "FEATURES=CGO, audio, no video, no opus"
if defined VIDEO_ON set "FEATURES=CGO, audio, video, no opus"
if defined AUDIO_ON set "FEATURES=CGO, audio, no video"
if defined VIDEO_ON if defined AUDIO_ON set "FEATURES=CGO, audio, video"
goto :eof

:video_setup
if not exist "third_party\video\%~1\lib" goto :video_none
if not exist "third_party\video\%~1\lib\libavcodec.a" goto :video_incomplete
if not exist "third_party\video\%~1\lib\libswscale.a" goto :video_incomplete
if not exist "third_party\video\%~1\lib\libavutil.a" goto :video_incomplete
if not exist "third_party\video\%~1\lib\libvpx.a" goto :video_incomplete
if not exist "third_party\video\%~1\lib\libopenh264.a" goto :video_incomplete
set "CGO_CFLAGS=%CGO_CFLAGS% -I%CD%\third_party\video\%~1\include"
set "CGO_LDFLAGS=%CGO_LDFLAGS% -L%CD%\third_party\video\%~1\lib"
set "VIDEO_ON=1"
if not defined BUILD_TAGS goto :video_first_tag
set "BUILD_TAGS=%BUILD_TAGS%,video"
goto :eof
:video_first_tag
set "BUILD_TAGS=video"
goto :eof
:video_none
echo       hint: third_party\video\%~1 not found, building without video
echo             run scripts\build-video-libs.bat once to enable it
goto :eof
:video_incomplete
echo       hint: third_party\video\%~1 is incomplete (a required .a is missing),
echo             building without video - re-run scripts\build-video-libs.bat
goto :eof

:audio_setup
if not exist "third_party\audio\%~1\lib" goto :audio_none
if not exist "third_party\audio\%~1\lib\libopus.a" goto :audio_incomplete
set "CGO_CFLAGS=%CGO_CFLAGS% -I%CD%\third_party\audio\%~1\include"
set "CGO_LDFLAGS=%CGO_LDFLAGS% -L%CD%\third_party\audio\%~1\lib"
set "AUDIO_ON=1"
if not defined BUILD_TAGS goto :audio_first_tag
set "BUILD_TAGS=%BUILD_TAGS%,audio"
goto :eof
:audio_first_tag
set "BUILD_TAGS=audio"
goto :eof
:audio_none
echo       hint: third_party\audio\%~1 not found, building without Opus
echo             run scripts\build-audio-lib.bat once to enable it
goto :eof
:audio_incomplete
echo       hint: third_party\audio\%~1 is incomplete (libopus.a is missing),
echo             building without Opus - re-run scripts\build-audio-lib.bat
goto :eof
