; Minimal NSIS installer skeleton. Build with makensis in a Windows release job.
Unicode true
Name "VKVM"
OutFile "VKVM-Setup.exe"
InstallDir "$PROGRAMFILES64\VKVM"
RequestExecutionLevel admin
Page directory
Page instfiles
Section "VKVM Core"
  SetOutPath "$INSTDIR"
  File "dist\vkvm.exe"
  FileOpen $0 "$INSTDIR\install.log" w
  FileWrite $0 "VKVM installer completed.$\r$\n"
  FileClose $0
  nsExec::ExecToLog 'sc.exe create VKVMCore binPath= "$INSTDIR\vkvm.exe --service" start= delayed-auto'
  nsExec::ExecToLog 'sc.exe failure VKVMCore reset= 86400 actions= restart/5000/restart/30000/restart/120000'
SectionEnd
