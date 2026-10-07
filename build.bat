@echo off
rem mti-sip-phone Windows build script: native builds, output to dist\
rem Depends: Go 1.24+, MinGW-W64 gcc (CGO; WinLibs or MSYS2)
rem NOTE: keep this file ASCII-only, cmd.exe parses bat as GBK on zh-CN Windows
rem Two build variants (see internal\variant and cmd\sip-phone\gui_noui.go):
rem   full  - complete app (Fyne desktop UI + system tray)   <- default
rem   noui  - tray-only build (-tags noui): no windows at all, every action goes
rem           through the web console (tray click opens the browser). It does not
rem           link fyne/GLFW at all, so no OpenGL/X11 is involved.
rem windows/386 GUI build needs 32-bit MinGW: MSYS2 (pacman -S mingw-w64-i686-gcc)
rem or WinLibs i686 at %USERPROFILE%\tools\winlibs-i686; without it the 386
rem target is skipped, there is no headless fallback anymore.
rem windows/arm64 GUI build needs llvm-mingw at
rem %USERPROFILE%\tools\llvm-mingw (or LLVM_MINGW=<dir>); GCC-based mingw-w64
rem has no aarch64-w64-mingw32 target. Skipped when absent. The video / Opus /
rem AEC static libraries are not built for arm64, so that target is always the
rem base build (G.711/G.722 only).
rem Linux GUI and Windows GUI cross builds from Linux are done by ./build.sh
rem Version: read from internal\core\buildinfo.go (never written here); dev builds
rem get "-rc", official builds are built by scripts\release.bat (SIP_PHONE_RELEASE=1)

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

rem App version: the base version lives in internal\core\buildinfo.go (single
rem source of truth, this script never writes it). Dev builds get "-rc" (not an
rem official release) so that publishing the official build with the same number
rem still triggers an update notification: 1.0.5-rc < 1.0.5, see
rem internal\updater\version.go. Official build: set SIP_PHONE_RELEASE=1
rem (scripts\release.bat does that; it also builds the publish package).
set "VER_BASE="
for /f "tokens=4" %%a in ('findstr /b /c:"var Version = " internal\core\buildinfo.go') do set "VER_BASE=%%~a"
if not defined VER_BASE goto :vererr
set "APP_VERSION=%VER_BASE%-rc"
if "%SIP_PHONE_RELEASE%"=="1" set "APP_VERSION=%VER_BASE%"
set "LDFLAGS=%LDFLAGS% -X mti-sip-phone/internal/core.Version=%APP_VERSION%"
if "%SIP_PHONE_RELEASE%"=="1" goto :ver_release
echo Version: %APP_VERSION% - dev build
goto :ver_done
:ver_release
echo Version: %APP_VERSION% - release build
:ver_done

rem ===============================================================
rem Windows targets. Every GUI target runs once per variant:
rem   dist\sip-phone-<os>-<arch>[.exe]           - full   (Fyne UI + tray)
rem   dist\sip-phone-no-ui-<os>-<arch>[.exe]     - noui   (tray only)
rem The console artifact (-headless server / command line debug) exists for the
rem full variant only: the tray-only build has no UI to debug and that artifact
rem is never published.
rem ===============================================================

call :variant_setup full
call :build_amd64
if errorlevel 1 goto :err
call :build_console
if errorlevel 1 goto :err
call :build_386
if errorlevel 1 goto :err
call :build_arm64
if errorlevel 1 goto :err

call :variant_setup noui
call :build_amd64
if errorlevel 1 goto :err
call :build_386
if errorlevel 1 goto :err
call :build_arm64
if errorlevel 1 goto :err

goto :summary

:summary
echo Build OK: version %APP_VERSION%
if defined AMD64_ON echo   dist\sip-phone-windows-amd64.exe           - tray GUI, static
if defined NOUI_AMD64_ON echo   dist\sip-phone-no-ui-windows-amd64.exe   - tray only, no UI, static
if defined CONSOLE_ON echo   dist\sip-phone-console.exe                 - console, headless/debug, static
if defined 386_ON echo   dist\sip-phone-windows-386.exe             - tray GUI, static
if defined NOUI_386_ON echo   dist\sip-phone-no-ui-windows-386.exe     - tray only, no UI, static
if defined ARM64_ON echo   dist\sip-phone-windows-arm64.exe           - tray GUI, base build
if defined NOUI_ARM64_ON echo   dist\sip-phone-no-ui-windows-arm64.exe   - tray only, no UI, base build
echo (skipped targets and their reasons are printed above)
echo Linux GUI and Windows GUI cross builds: run ./build.sh on Linux
exit /b 0

:err
echo Build FAILED
exit /b 1

:vererr
echo Build FAILED: cannot parse base version from internal\core\buildinfo.go
exit /b 1

rem ---------------------------------------------------------------
rem Build variant + per-target subroutines (called from the block above).
rem ---------------------------------------------------------------

:variant_setup
rem %~1 = full | noui. Sets the artifact infix, the extra build tag and the
rem summary flag prefix. Variables are read inside the called subroutines
rem (which are parsed when they run), never inside a parenthesized block of
rem the caller - that is where cmd's delayed expansion would bite.
set "VAR_INFIX="
set "VAR_TAGS_EXTRA="
set "VAR_LABEL=full"
set "VAR_FLAG="
if not "%~1"=="noui" goto :eof
set "VAR_INFIX=no-ui-"
set "VAR_TAGS_EXTRA=noui"
set "VAR_LABEL=no-ui"
set "VAR_FLAG=NOUI_"
goto :eof

:build_amd64
rem Native amd64: no console (GUI subsystem), static mingw runtime.
rem -extldflags -static: link the mingw runtimes (libwinpthread/libstdc++/libgcc)
rem into the exe. Without it the binary needs those DLLs on PATH -- they are NOT
rem part of Windows, so a copy of the exe on a machine without MinGW fails to
rem start ("libwinpthread-1.dll not found"). Same as the 386 target.
rem GOOS/GOARCH/CC/CXX are set explicitly on every target: a previous 386 or
rem arm64 build leaves its own values behind, and the noui pass runs after them.
set CGO_ENABLED=1
set GOOS=windows
set GOARCH=amd64
if defined GCC (set "CC=%GCC%") else (set "CC=")
if defined GCC (set "CXX=%GCC:gcc.exe=g++.exe%") else (set "CXX=")
call :codec_setup windows-amd64 %VAR_TAGS_EXTRA%
if errorlevel 1 exit /b 1
echo [%VAR_LABEL%] Building dist\sip-phone-%VAR_INFIX%windows-amd64.exe (%FEATURES%, static)...
go build %TAGS_ARG% -ldflags "%LDFLAGS% -H=windowsgui -extldflags -static" -o dist\sip-phone-%VAR_INFIX%windows-amd64.exe .\cmd\sip-phone
if errorlevel 1 exit /b 1
set "%VAR_FLAG%AMD64_ON=1"
exit /b 0

:build_console
rem Console subsystem build: runs with -headless (WS service only) or as a
rem command line debug build. Full variant only (see the note above).
set CGO_ENABLED=1
set GOOS=windows
set GOARCH=amd64
if defined GCC (set "CC=%GCC%") else (set "CC=")
if defined GCC (set "CXX=%GCC:gcc.exe=g++.exe%") else (set "CXX=")
call :codec_setup windows-amd64
if errorlevel 1 exit /b 1
echo [full] Building dist\sip-phone-console.exe (%FEATURES%, console/headless, static)...
go build %TAGS_ARG% -ldflags "%LDFLAGS% -extldflags -static" -o dist\sip-phone-console.exe .\cmd\sip-phone
if errorlevel 1 exit /b 1
set "CONSOLE_ON=1"
exit /b 0

:build_386
rem 32-bit build: needs 32-bit MinGW, otherwise skipped.
set "GCC32="
if exist "C:\msys64\mingw32\bin\i686-w64-mingw32-gcc.exe" set "GCC32=C:\msys64\mingw32\bin\i686-w64-mingw32-gcc.exe"
if exist "%USERPROFILE%\tools\winlibs-i686\mingw32\bin\i686-w64-mingw32-gcc.exe" set "GCC32=%USERPROFILE%\tools\winlibs-i686\mingw32\bin\i686-w64-mingw32-gcc.exe"
rem NOTE: no parentheses or trailing "..." in echo lines inside if/else blocks,
rem cmd.exe mis-parses them as a command group and aborts with a syntax error.
if not defined GCC32 goto :build_386_skip
set CGO_ENABLED=1
set CC=%GCC32%
rem Audio preprocessing's C++ bridge (internal/audio/apm_bridge.cc) and the
rem camera source need a CXX of the SAME architecture as CC: Go's default is a
rem bare "g++" which resolves to the x64 toolchain on PATH, so a 32-bit target
rem would link with the wrong one. Derive it from CC by swapping the suffix.
set "CXX=%GCC32:gcc.exe=g++.exe%"
set GOOS=windows
set GOARCH=386
call :codec_setup windows-386 %VAR_TAGS_EXTRA%
if errorlevel 1 exit /b 1
echo [%VAR_LABEL%] Building dist\sip-phone-%VAR_INFIX%windows-386.exe (%FEATURES%, static)...
go build %TAGS_ARG% -ldflags "%LDFLAGS% -H=windowsgui -extldflags -static" -o dist\sip-phone-%VAR_INFIX%windows-386.exe .\cmd\sip-phone
if errorlevel 1 exit /b 1
set "%VAR_FLAG%386_ON=1"
exit /b 0

:build_386_skip
echo [%VAR_LABEL%] i686 gcc not found, skipping dist\sip-phone-%VAR_INFIX%windows-386.exe
echo       install MSYS2 mingw-w64-i686-gcc or WinLibs i686 to enable it
exit /b 0

:build_arm64
rem Needs llvm-mingw (LLVM/Clang based): GCC-based mingw-w64 has no
rem aarch64-w64-mingw32 target, so the usual MinGW toolchain cannot produce this
rem one. Skipped when not installed.
set "LLVM_BIN="
set "LLVM_HINT="
if defined LLVM_MINGW set "LLVM_BIN=%LLVM_MINGW%\bin"
if defined LLVM_MINGW set "LLVM_HINT=LLVM_MINGW=%LLVM_MINGW%"
if not defined LLVM_BIN if exist "%USERPROFILE%\tools\llvm-mingw\bin\aarch64-w64-mingw32-clang.exe" set "LLVM_BIN=%USERPROFILE%\tools\llvm-mingw\bin"
if not defined LLVM_BIN goto :build_arm64_skip
if not exist "%LLVM_BIN%\aarch64-w64-mingw32-clang.exe" goto :build_arm64_skip
set CGO_ENABLED=1
set "CC=%LLVM_BIN%\aarch64-w64-mingw32-clang.exe"
set "CXX=%LLVM_BIN%\aarch64-w64-mingw32-clang++.exe"
set GOOS=windows
set GOARCH=arm64
rem No codec probing here on purpose: the video / Opus / AEC static libraries
rem are not built for windows-arm64 (and build-video-libs.bat cannot produce
rem them), so the hints printed for the x86 targets would be misleading.
rem This target is always the base build: CGO + audio devices + G.711/G.722.
set "TAGS_ARG="
set "CGO_CFLAGS="
set "CGO_CXXFLAGS="
set "CGO_LDFLAGS="
if defined VAR_TAGS_EXTRA set "TAGS_ARG=-tags %VAR_TAGS_EXTRA%"
echo [%VAR_LABEL%] Building dist\sip-phone-%VAR_INFIX%windows-arm64.exe (CGO, audio, no video/opus/AEC)...
echo       note: base build only - no arm64 static libraries for video/Opus/AEC
go build %TAGS_ARG% -ldflags "%LDFLAGS% -H=windowsgui" -o dist\sip-phone-%VAR_INFIX%windows-arm64.exe .\cmd\sip-phone
if errorlevel 1 exit /b 1
set "%VAR_FLAG%ARM64_ON=1"
exit /b 0

:build_arm64_skip
echo [%VAR_LABEL%] llvm-mingw not found, skipping dist\sip-phone-%VAR_INFIX%windows-arm64.exe
echo       install: download llvm-mingw-^<version^>-ucrt-x86_64.zip from
echo         https://github.com/mstorsjo/llvm-mingw/releases
echo       unpack it to %%USERPROFILE%%\tools\llvm-mingw so that
echo         %%USERPROFILE%%\tools\llvm-mingw\bin\aarch64-w64-mingw32-clang.exe exists
echo       or set LLVM_MINGW to an existing llvm-mingw directory
echo       note: GCC-based mingw-w64 has no aarch64 target, llvm-mingw is required
exit /b 0

rem ---------------------------------------------------------------
rem Codec static libraries, built once by the matching script:
rem   third_party\video\<target>  <- scripts\build-video-libs.bat  -> -tags video
rem   third_party\audio\<target>  <- scripts\build-audio-libs.bat  -> -tags audio
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
rem
rem %~1 = target (windows-amd64 / windows-386), %~2 = extra build tag for the
rem variant (noui for the tray-only build, empty for the full one).
rem ---------------------------------------------------------------
:codec_setup
set "TAGS_ARG="
set "BUILD_TAGS="
set "VIDEO_ON="
set "AUDIO_ON="
set "APM_ON="
set "FEATURES="
set "CGO_CFLAGS="
set "CGO_CXXFLAGS="
set "CGO_LDFLAGS="
call :video_setup %~1
call :audio_setup %~1
call :apm_setup %~1
if not "%~2"=="" goto :codec_variant_tag
if defined BUILD_TAGS set "TAGS_ARG=-tags %BUILD_TAGS%"
goto :codec_features
:codec_variant_tag
if defined BUILD_TAGS (
    set "BUILD_TAGS=%BUILD_TAGS%,%~2"
) else (
    set "BUILD_TAGS=%~2"
)
set "TAGS_ARG=-tags %BUILD_TAGS%"
:codec_features
rem FEATURES: what this target really gets (no codec lists - audio is the
rem base and Opus is part of audio; video and Opus come from their static
rem libraries). All four combinations spelled out; how to add a missing one
rem is in the hint lines above.
set "FEATURES=CGO, audio, no video, no opus"
if defined VIDEO_ON set "FEATURES=CGO, audio, video, no opus"
if defined AUDIO_ON set "FEATURES=CGO, audio, no video"
if defined VIDEO_ON if defined AUDIO_ON set "FEATURES=CGO, audio, video"
rem Audio preprocessing (AEC/NS/AGC, REQ-MEDIA-007) is listed separately:
rem it degrades independently of Opus.
if defined APM_ON set "FEATURES=%FEATURES%, AEC"
if not "%~2"=="" set "FEATURES=%FEATURES%, no UI"
goto :eof

:apm_setup
rem %~1 = target name (windows-amd64 / windows-386); sits in the same
rem third_party\audio dir as opus but gets its own build tag.
if not exist "third_party\audio\%~1\lib\libwebrtc-audio-processing-1.a" goto :apm_none
if not exist "third_party\audio\%~1\lib\libwebrtc-audio-coding-1.a" goto :apm_none
set "CGO_CFLAGS=%CGO_CFLAGS% -I%CD%\third_party\audio\%~1\include"
rem headers live under include\webrtc-audio-processing-1 (meson version suffix)
set "CGO_CFLAGS=%CGO_CFLAGS% -I%CD%\third_party\audio\%~1\include\webrtc-audio-processing-1"
rem C++ sources (apm_bridge.cc) are compiled with CGO_CXXFLAGS, not CGO_CFLAGS
set "CGO_CXXFLAGS=%CGO_CXXFLAGS% -I%CD%\third_party\audio\%~1\include"
set "CGO_CXXFLAGS=%CGO_CXXFLAGS% -I%CD%\third_party\audio\%~1\include\webrtc-audio-processing-1"
set "CGO_LDFLAGS=%CGO_LDFLAGS% -L%CD%\third_party\audio\%~1\lib"
set "APM_ON=1"
if not defined BUILD_TAGS goto :apm_first_tag
set "BUILD_TAGS=%BUILD_TAGS%,apm"
goto :eof
:apm_first_tag
set "BUILD_TAGS=apm"
goto :eof
:apm_none
echo       hint: audio preprocessing (AEC/NS/AGC) not found under third_party\audio\%~1,
echo             building without it - run scripts\build-audio-libs.bat once to enable it
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
echo             run scripts\build-audio-libs.bat once to enable it
goto :eof
:audio_incomplete
echo       hint: third_party\audio\%~1 is incomplete (libopus.a is missing),
echo             building without Opus - re-run scripts\build-audio-libs.bat
goto :eof
