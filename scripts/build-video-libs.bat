@echo off
rem 视频编解码静态库构建入口（Windows）。
rem 实际逻辑在 build-video-libs.sh，需要 git-bash：git 安装时自带，
rem 或用 WinLibs/MSYS2 的 bash。默认只构建 windows-amd64；
rem 传 "all" 构建全部四个目标（缺工具链的自动跳过）。
rem
rem 用法：
rem   scripts\build-video-libs.bat              -> windows-amd64
rem   scripts\build-video-libs.bat all          -> 全部目标
rem   scripts\build-video-libs.bat --clean windows-amd64
setlocal

set "BASH="
if exist "%ProgramFiles%\Git\bin\bash.exe" set "BASH=%ProgramFiles%\Git\bin\bash.exe"
if not defined BASH if exist "%ProgramFiles(x86)%\Git\bin\bash.exe" set "BASH=%ProgramFiles(x86)%\Git\bin\bash.exe"
if not defined BASH if exist "%LOCALAPPDATA%\Programs\Git\bin\bash.exe" set "BASH=%LOCALAPPDATA%\Programs\Git\bin\bash.exe"
if not defined BASH for %%b in (bash.exe) do if not defined BASH set "BASH=%%~$PATH:b"
if not defined BASH goto :nobash

rem 把 WinLibs 的 mingw64\bin 放进 PATH（nasm/yasm/mingw32-make/gcc 都在里面）
for /d %%d in ("%LOCALAPPDATA%\Microsoft\WinGet\Packages\BrechtSanders.WinLibs.POSIX.UCRT_*") do (
    if exist "%%d\mingw64\bin\gcc.exe" set "PATH=%%d\mingw64\bin;%PATH%"
)

set "SCRIPT=%~dp0build-video-libs.sh"
"%BASH%" -lc "exec \"$(cygpath -u '%SCRIPT%' 2>/dev/null || echo '%SCRIPT%')\" %*"
if errorlevel 1 goto :err
exit /b 0

:nobash
echo 找不到 bash。请安装 Git for Windows（自带 git-bash），或把 bash.exe 放进 PATH。
exit /b 1

:err
echo 构建失败。
exit /b 1
