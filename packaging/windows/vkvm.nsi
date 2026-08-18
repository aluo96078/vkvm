; VKVM Windows installer. Build with makensis in the Windows release job or
; with a macOS-hosted makensis cross-packager.
Unicode true
!include LogicLib.nsh

!define APP_NAME "VKVM"
!define APP_VERSION "0.2.0"

Name "${APP_NAME}"
OutFile "..\\..\\VKVM-Setup.exe"
InstallDir "$PROGRAMFILES64\\VKVM"
InstallDirRegKey HKLM "Software\\VKVM" "InstallDir"
RequestExecutionLevel admin
ShowInstDetails show
ShowUnInstDetails show
BrandingText "VKVM"
VIProductVersion "${APP_VERSION}.0"
VIAddVersionKey /LANG=1033 "ProductName" "VKVM"
VIAddVersionKey /LANG=1033 "ProductVersion" "${APP_VERSION}"
VIAddVersionKey /LANG=1033 "FileVersion" "${APP_VERSION}.0"
VIAddVersionKey /LANG=1033 "FileDescription" "VKVM KVM switcher"
VIAddVersionKey /LANG=1033 "CompanyName" "VKVM"
VIAddVersionKey /LANG=1033 "LegalCopyright" "VKVM"

Page directory
Page instfiles
UninstPage uninstConfirm
UninstPage instfiles

Section "VKVM Core" SEC_CORE
  SetShellVarContext current
  SetOutPath "$INSTDIR"
  ; Paths are relative to this script's directory so Windows CI and
  ; macOS-hosted makensis resolve the repository-level dist/ output.
  File "..\\..\\dist\\vkvm.exe"

  ; The SCM service runs as LocalSystem, while the companion runs as the
  ; logged-in user. Share one protected machine configuration store. The
  ; built-in Users SID is granted modify access so the current user's UI can
  ; save settings; the service already has LocalSystem full control.
  ReadEnvStr $1 "ProgramData"
  StrCmp $1 "" use_appdata_config
  StrCpy $2 "$1\\VKVM"
  Goto config_dir_ready
use_appdata_config:
  StrCpy $2 "$APPDATA\\VKVM"
config_dir_ready:
  CreateDirectory "$2"
  nsExec::ExecToLog '"$SYSDIR\\icacls.exe" "$2" /grant *S-1-5-32-545:(OI)(CI)M /inheritance:e'
  Pop $0
  IfFileExists "$2\\config.json" skip_config_migration
  IfFileExists "$APPDATA\\vkvm\\config.json" 0 skip_config_migration
  CopyFiles /SILENT "$APPDATA\\vkvm\\config.json" "$2"
skip_config_migration:

  WriteUninstaller "$INSTDIR\\uninstall.exe"
  WriteRegStr HKLM "Software\\VKVM" "InstallDir" "$INSTDIR"
  WriteRegStr HKLM "Software\\Microsoft\\Windows\\CurrentVersion\\Uninstall\\VKVM" "DisplayName" "VKVM"
  WriteRegStr HKLM "Software\\Microsoft\\Windows\\CurrentVersion\\Uninstall\\VKVM" "DisplayVersion" "${APP_VERSION}"
  WriteRegStr HKLM "Software\\Microsoft\\Windows\\CurrentVersion\\Uninstall\\VKVM" "Publisher" "VKVM"
  WriteRegStr HKLM "Software\\Microsoft\\Windows\\CurrentVersion\\Uninstall\\VKVM" "InstallLocation" "$INSTDIR"
  WriteRegStr HKLM "Software\\Microsoft\\Windows\\CurrentVersion\\Uninstall\\VKVM" "UninstallString" "$INSTDIR\\uninstall.exe"
  WriteRegDWORD HKLM "Software\\Microsoft\\Windows\\CurrentVersion\\Uninstall\\VKVM" "NoModify" 1
  WriteRegDWORD HKLM "Software\\Microsoft\\Windows\\CurrentVersion\\Uninstall\\VKVM" "NoRepair" 1

  ; Normalize fresh installs and upgrades to an enabled companion startup
  ; setting. This preserves the user's ability to turn it off later while
  ; fixing old configs that explicitly stored start_on_boot=false.
  nsExec::ExecToLog '"$INSTDIR\\vkvm.exe" --enable-autostart'
  Pop $0

  CreateDirectory "$SMPROGRAMS\\VKVM"
  CreateShortCut "$SMPROGRAMS\\VKVM\\VKVM.lnk" "$INSTDIR\\vkvm.exe" "--companion" "$INSTDIR\\vkvm.exe" 0
  CreateShortCut "$DESKTOP\\VKVM.lnk" "$INSTDIR\\vkvm.exe" "--companion" "$INSTDIR\\vkvm.exe" 0

  ; The companion belongs to the logged-in user, while the authenticated
  ; network core is hosted by the SCM service. Keep the user-session launcher
  ; in HKCU so it starts after login without running hooks as LocalSystem.
  WriteRegStr HKCU "Software\\Microsoft\\Windows\\CurrentVersion\\Run" "VKVM" "$\"$INSTDIR\\vkvm.exe$\" --companion"

  FileOpen $0 "$INSTDIR\\install.log" w
  FileWrite $0 "VKVM installer completed (version ${APP_VERSION}).$\r$\n"
  FileClose $0

  ; Reinstall/update the service with a correctly quoted executable path.
  ; sc.exe requires a space after each option '=' and the binary path must
  ; quote Program Files paths while leaving --service outside the quotes.
  nsExec::ExecToLog '"$SYSDIR\\sc.exe" stop VKVMCore'
  Pop $0
  nsExec::ExecToLog '"$SYSDIR\\sc.exe" delete VKVMCore'
  Pop $0
  nsExec::ExecToLog '"$SYSDIR\\sc.exe" create VKVMCore binPath= "\"$INSTDIR\\vkvm.exe\" --service" start= delayed-auto DisplayName= "VKVM Core"'
  Pop $0
  nsExec::ExecToLog '"$SYSDIR\\sc.exe" failure VKVMCore reset= 86400 actions= restart/5000/restart/30000/restart/120000'
  Pop $0
  nsExec::ExecToLog '"$SYSDIR\\sc.exe" description VKVMCore "VKVM authenticated network and profile core"'
  Pop $0
  nsExec::ExecToLog '"$SYSDIR\\sc.exe" start VKVMCore'
  Pop $0
SectionEnd

Section "Uninstall"
  SetShellVarContext current
  nsExec::ExecToLog '"$SYSDIR\\sc.exe" stop VKVMCore'
  Pop $0
  nsExec::ExecToLog '"$SYSDIR\\sc.exe" delete VKVMCore'
  Pop $0
  DeleteRegValue HKCU "Software\\Microsoft\\Windows\\CurrentVersion\\Run" "VKVM"
  DeleteRegKey HKLM "Software\\Microsoft\\Windows\\CurrentVersion\\Uninstall\\VKVM"
  DeleteRegKey HKLM "Software\\VKVM"
  Delete "$SMPROGRAMS\\VKVM\\VKVM.lnk"
  RMDir "$SMPROGRAMS\\VKVM"
  Delete "$DESKTOP\\VKVM.lnk"
  Delete "$INSTDIR\\install.log"
  Delete "$INSTDIR\\uninstall.exe"
  Delete "$INSTDIR\\vkvm.exe"
  RMDir "$INSTDIR"
SectionEnd
