; BuddyBot Windows 安装器（NSIS）
; 构建：makensis /DARCH=amd64 /DBINDIR=bin build\windows\installer.nsi
; 产物：bin\BuddyBot-windows-${ARCH}-installer.exe
; 与便携版 BuddyBot-windows-${ARCH}.exe 名称区分开。

!ifndef ARCH
  !define ARCH "amd64"
!endif
!ifndef BINDIR
  !define BINDIR "bin"
!endif

Unicode true
ManifestDPIAware true

!include "MUI2.nsh"

Name "BuddyBot"
OutFile "${BINDIR}\BuddyBot-windows-${ARCH}-installer.exe"
InstallDir "$PROGRAMFILES64\BuddyBot"
InstallDirRegKey HKLM "Software\BuddyBot" "InstallDir"
RequestExecutionLevel admin

!define MUI_ABORTWARNING
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "SimpChinese"

; 安装/卸载前先结束正在运行的实例（BuddyBot 有单实例锁，直接覆盖会失败）

Section "Install"
  nsExec::ExecToLog 'taskkill /IM BuddyBot.exe /F'
  SetOutPath "$INSTDIR"
  File "${BINDIR}/BuddyBot.exe"
  WriteRegStr HKLM "Software\BuddyBot" "InstallDir" "$INSTDIR"
  WriteUninstaller "$INSTDIR\Uninstall.exe"

  ; 开始菜单快捷方式
  CreateDirectory "$SMPROGRAMS\BuddyBot"
  CreateShortcut "$SMPROGRAMS\BuddyBot\BuddyBot.lnk" "$INSTDIR\BuddyBot.exe"
  CreateShortcut "$SMPROGRAMS\BuddyBot\Uninstall BuddyBot.lnk" "$INSTDIR\Uninstall.exe"
  ; 桌面快捷方式
  CreateShortcut "$DESKTOP\BuddyBot.lnk" "$INSTDIR\BuddyBot.exe"

  ; 控制面板「程序和功能」卸载入口
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\BuddyBot" \
    "DisplayName" "BuddyBot"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\BuddyBot" \
    "UninstallString" "$INSTDIR\Uninstall.exe"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\BuddyBot" \
    "DisplayIcon" "$INSTDIR\BuddyBot.exe"
SectionEnd

Section "Uninstall"
  nsExec::ExecToLog 'taskkill /IM BuddyBot.exe /F'
  Delete "$INSTDIR\BuddyBot.exe"
  Delete "$INSTDIR\Uninstall.exe"
  RMDir "$INSTDIR"
  RMDir "$SMPROGRAMS\BuddyBot"
  Delete "$DESKTOP\BuddyBot.lnk"
  DeleteRegKey HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\BuddyBot"
  DeleteRegKey HKLM "Software\BuddyBot"
SectionEnd
