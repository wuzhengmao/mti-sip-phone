@echo off
rem 一键发布入口（Windows）。实际逻辑在 release.sh，需要 git-bash：git 安装时自带。
rem
rem 用法：
rem   scripts\release.bat                  -> 正式版（SIP_PHONE_RELEASE=1 编译，版本如 1.0.5）
rem   scripts\release.bat --pre            -> 预发布版（版本 1.0.5-rc，站点标记为预发布）
rem   scripts\release.bat --pre --dry-run  -> 只编译打包，不上传
rem   scripts\release.bat --no-build       -> 用 dist/ 里已有产物打包
rem
rem 配置在 release\release.conf（站点地址 / 应用 ID / 上传令牌 / 归档目录）。
rem 参数原样透传给 release.sh，含空格的参数请自带引号。
setlocal

set "BASH="
if exist "%ProgramFiles%\Git\bin\bash.exe" set "BASH=%ProgramFiles%\Git\bin\bash.exe"
if not defined BASH if exist "%ProgramFiles(x86)%\Git\bin\bash.exe" set "BASH=%ProgramFiles(x86)%\Git\bin\bash.exe"
if not defined BASH if exist "%LOCALAPPDATA%\Programs\Git\bin\bash.exe" set "BASH=%LOCALAPPDATA%\Programs\Git\bin\bash.exe"
if not defined BASH for %%b in (bash.exe) do if not defined BASH set "BASH=%%~$PATH:b"
if not defined BASH goto :nobash

set "SCRIPT=%~dp0release.sh"
"%BASH%" -lc "exec \"$(cygpath -u '%SCRIPT%' 2>/dev/null || echo '%SCRIPT%')\" %*"
if errorlevel 1 goto :err
exit /b 0

:nobash
echo 找不到 bash。请安装 Git for Windows（自带 git-bash），或把 bash.exe 放进 PATH。
exit /b 1

:err
echo 发布失败。
exit /b 1
