@echo off
REM VKVM Input Capture Diagnostics Script
REM This script helps diagnose input capture issues on Windows

echo VKVM Input Capture Diagnostics
echo ===============================
echo.

echo Checking system information...
echo.

REM Check if running as administrator
net session >nul 2>&1
if %errorLevel% == 0 (
    echo [OK] Running as Administrator
) else (
    echo [ERROR] Not running as Administrator
    echo Raw Input capture requires Administrator privileges.
    echo Please right-click the executable and select "Run as administrator"
)

echo.
echo Checking for conflicting processes...
echo.

REM Check for known conflicting applications
set CONFLICTS=0

tasklist /FI "IMAGENAME eq barrier.exe" 2>NUL | find /I "barrier.exe" >NUL
if %ERRORLEVEL% EQU 0 (
    echo [WARNING] Found Barrier (conflicting KVM software)
    set /A CONFLICTS+=1
)

tasklist /FI "IMAGENAME eq synergy.exe" 2>NUL | find /I "synergy.exe" >NUL
if %ERRORLEVEL% EQU 0 (
    echo [WARNING] Found Synergy (conflicting KVM software)
    set /A CONFLICTS+=1
)

tasklist /FI "IMAGENAME eq teamviewer.exe" 2>NUL | find /I "teamviewer.exe" >NUL
if %ERRORLEVEL% EQU 0 (
    echo [WARNING] Found TeamViewer (remote control software)
    set /A CONFLICTS+=1
)

tasklist /FI "IMAGENAME eq anydesk.exe" 2>NUL | find /I "anydesk.exe" >NUL
if %ERRORLEVEL% EQU 0 (
    echo [WARNING] Found AnyDesk (remote control software)
    set /A CONFLICTS+=1
)

if %CONFLICTS% EQU 0 (
    echo [OK] No obvious conflicting processes found
)

echo.
echo Checking mouse devices...
echo.

REM Check for HID mouse devices
pnputil /enum-devices /class Mouse 2>NUL | find "Mouse" >NUL
if %ERRORLEVEL% EQU 0 (
    echo [OK] Found mouse devices in Device Manager
) else (
    echo [WARNING] No mouse devices found in Device Manager
)

echo.
echo Checking network connectivity...
echo.

REM Check if we can reach common local IPs
ping -n 1 -w 1000 192.168.1.1 >NUL 2>&1
if %ERRORLEVEL% EQU 0 (
    echo [OK] Network connectivity appears normal
) else (
    echo [WARNING] Network connectivity issues detected
)

echo.
echo Recommendations:
echo ================
echo.

if %CONFLICTS% GTR 0 (
    echo - Close conflicting applications before running VKVM
)

echo - Ensure you're running as Administrator
echo - Try a different USB port for your mouse
echo - Check Device Manager for mouse driver issues
echo - Test with a different mouse if possible
echo - Run the test and check the detailed log output

echo.
echo Press any key to exit...
pause >NUL