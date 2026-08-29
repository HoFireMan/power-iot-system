@echo off
setlocal
set "POWER_IOT_REPO=/home/admin-195/code/power-iot-system"
set "POWER_IOT_WSL_DISTRO_ARG="
if defined POWER_IOT_WSL_DISTRO set "POWER_IOT_WSL_DISTRO_ARG=--distribution %POWER_IOT_WSL_DISTRO%"

wsl.exe %POWER_IOT_WSL_DISTRO_ARG% -- bash -lc "cd '%POWER_IOT_REPO%' && exec ./scripts/local-runtime.sh %*"
set "EXIT_CODE=%ERRORLEVEL%"
endlocal & exit /b %EXIT_CODE%
