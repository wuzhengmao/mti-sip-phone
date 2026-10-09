@echo off
rem һ��������ڣ�Windows����ʵ���߼��� release.sh����Ҫ git-bash��git ��װʱ�Դ���
rem
rem �÷���
rem   scripts\release.bat                  -> ��ʽ�棨SIP_PHONE_RELEASE=1 ���룬�汾�� 1.0.5��
rem   scripts\release.bat --pre            -> Ԥ�����棨�汾 1.0.5-rc��վ����ΪԤ������
rem   scripts\release.bat --pre --dry-run  -> ֻ�����������ϴ�
rem   scripts\release.bat --no-build       -> �� dist/ �����в�����
rem
rem ������ release\release.conf��վ���ַ / Ӧ�� ID / �ϴ����� / �鵵Ŀ¼����
rem ����ԭ��͸���� release.sh�����ո�Ĳ������Դ����š�
setlocal

set "BASH="
if exist "%ProgramFiles%\Git\bin\bash.exe" set "BASH=%ProgramFiles%\Git\bin\bash.exe"
if not defined BASH if exist "%ProgramFiles(x86)%\Git\bin\bash.exe" set "BASH=%ProgramFiles(x86)%\Git\bin\bash.exe"
if not defined BASH if exist "%LOCALAPPDATA%\Programs\Git\bin\bash.exe" set "BASH=%LOCALAPPDATA%\Programs\Git\bin\bash.exe"
if not defined BASH for %%b in (bash.exe) do if not defined BASH set "BASH=%%~$PATH:b"
if not defined BASH goto :nobash

set "SCRIPT=%~dp0release.sh"
rem NOTE: cmd does not treat \" as an escape -- the previous
rem   "exec \"$(cygpath -u '%SCRIPT%' ...)\"" form broke the quoting (cmd saw
rem   $(cygpath as a new command and || as its own operator) and never worked.
rem Simple form: forward-slash path (bash accepts D:/...) inside single quotes.
set "SCRIPT=%SCRIPT:\=/%"
"%BASH%" -lc "exec '%SCRIPT%' %*"
if errorlevel 1 goto :err
exit /b 0

:nobash
rem �����Ӣ�ģ�cmd �� ANSI/GBK ����ҳ�� .bat��UTF-8 ���Ļ���ʾ������
echo bash.exe not found. Install Git for Windows (bundles git-bash) or put bash.exe on PATH.
exit /b 1

:err
echo Release failed.
exit /b 1
