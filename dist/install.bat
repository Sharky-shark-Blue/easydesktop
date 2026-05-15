@echo off
setlocal

set "SRC=%~dp0easydesktop.exe"
set "GUI_SRC=%~dp0easydesktop-ui.exe"
set "DST_DIR=%LocalAppData%\EasyDesktop"
set "DST=%DST_DIR%\easydesktop.exe"
set "GUI_DST=%DST_DIR%\easydesktop-ui.exe"

if not exist "%SRC%" (
  echo easydesktop.exe not found next to installer.
  pause
  exit /b 1
)

if not exist "%GUI_SRC%" (
  echo easydesktop-ui.exe not found next to installer.
  pause
  exit /b 1
)

if not exist "%DST_DIR%" mkdir "%DST_DIR%"
copy /Y "%SRC%" "%DST%" >nul
copy /Y "%GUI_SRC%" "%GUI_DST%" >nul

set "USER_PATH="
for /f "tokens=*" %%i in ('powershell -NoProfile -Command "[Environment]::GetEnvironmentVariable('Path','User')"') do set "USER_PATH=%%i"

echo %USER_PATH% | find /I "%DST_DIR%" >nul
if errorlevel 1 (
  powershell -NoProfile -Command "[Environment]::SetEnvironmentVariable('Path', ([Environment]::GetEnvironmentVariable('Path','User').TrimEnd(';') + ';%DST_DIR%'), 'User')"
)

echo Installed. Reopen terminal, then run: easydesktop
echo Optional: easydesktop D:\Work
echo Optional: easydesktop --gui
pause
