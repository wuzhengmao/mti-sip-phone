@echo off
rem ��Ƶ����뾲̬�⹹����ڣ�Windows����
rem ʵ���߼��� build-audio-libs.sh����Ҫ git-bash��git ��װʱ�Դ���
rem ���� WinLibs/MSYS2 �� bash��Ĭ��ֻ���� windows-amd64��
rem �� "all" ����ȫ���ĸ�Ŀ�꣨ȱ���������Զ���������
rem
rem �÷���
rem   scripts\build-audio-libs.bat              -> windows-amd64
rem   scripts\build-audio-libs.bat all          -> ȫ��Ŀ��
rem   scripts\build-audio-libs.bat --clean windows-amd64
setlocal

set "BASH="
if exist "%ProgramFiles%\Git\bin\bash.exe" set "BASH=%ProgramFiles%\Git\bin\bash.exe"
if not defined BASH if exist "%ProgramFiles(x86)%\Git\bin\bash.exe" set "BASH=%ProgramFiles(x86)%\Git\bin\bash.exe"
if not defined BASH if exist "%LOCALAPPDATA%\Programs\Git\bin\bash.exe" set "BASH=%LOCALAPPDATA%\Programs\Git\bin\bash.exe"
if not defined BASH for %%b in (bash.exe) do if not defined BASH set "BASH=%%~$PATH:b"
if not defined BASH goto :nobash

rem �� WinLibs �� mingw64\bin �Ž� PATH��mingw32-make/gcc �������棩
for /d %%d in ("%LOCALAPPDATA%\Microsoft\WinGet\Packages\BrechtSanders.WinLibs.POSIX.UCRT_*") do (
    if exist "%%d\mingw64\bin\gcc.exe" set "PATH=%%d\mingw64\bin;%PATH%"
)

set "SCRIPT=%~dp0build-audio-libs.sh"
rem NOTE: cmd does not treat \" as an escape -- the previous
rem   "exec \"$(cygpath -u '%SCRIPT%' ...)\"" form broke the quoting (cmd saw
rem   $(cygpath as a new command and || as its own operator) and never worked.
rem Simple form: forward-slash path (bash accepts D:/...) inside single quotes.
set "SCRIPT=%SCRIPT:\=/%"
"%BASH%" -lc "exec '%SCRIPT%' %*"
if errorlevel 1 goto :err
exit /b 0

:nobash
echo �Ҳ��� bash���밲װ Git for Windows���Դ� git-bash������� bash.exe �Ž� PATH��
exit /b 1

:err
echo ����ʧ�ܡ�
exit /b 1
