; HELINK's installer: for the user only (no administrator), in %LOCALAPPDATA%\Programs\HELINK,
; with Start menu and desktop shortcuts and an entry in "Installed apps". HELINK itself puts
; its helper for the browsers and the extension in place when it starts (integrate_windows.go).
; Build: makensis -DVERSION=2.4.0 -DEXE=../build/HELINK.exe -DOUT=../release/HELINK-2.4.0-Setup.exe helink.nsi
;
; HELINK updates itself with this installer (update.go): "Setup.exe /S /RELAUNCH" installs quietly
; over the running copy (which it asks to close, and waits for) and opens the new HELINK.

; HELINK is 64-bit only (Windows 10 and 11, 64-bit): so is its installer.
Target amd64-unicode
ManifestDPIAware true
SetCompressor /SOLID lzma
RequestExecutionLevel user

!ifndef VERSION
  !define VERSION "2.4.0"
!endif
!ifndef EXE
  !define EXE "..\build\HELINK.exe"
!endif
!ifndef OUT
  !define OUT "..\release\HELINK-${VERSION}-Setup.exe"
!endif
!define UNKEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\HELINK"

Name "HELINK"
OutFile "${OUT}"
InstallDir "$LOCALAPPDATA\Programs\HELINK"
InstallDirRegKey HKCU "Software\HELINK" "InstallDir"
BrandingText "HELINK ${VERSION}"
ShowInstDetails nevershow
ShowUninstDetails nevershow

VIProductVersion "${VERSION}.0"
VIAddVersionKey /LANG=0 "ProductName" "HELINK"
VIAddVersionKey /LANG=0 "CompanyName" "Helal Technology"
VIAddVersionKey /LANG=0 "FileDescription" "HELINK Setup"
VIAddVersionKey /LANG=0 "FileVersion" "${VERSION}"
VIAddVersionKey /LANG=0 "ProductVersion" "${VERSION}"
VIAddVersionKey /LANG=0 "LegalCopyright" "© 2026 Ali Hossam. HELINK is a trademark of Ali Hossam."

!include "MUI2.nsh"
!include "FileFunc.nsh"

Var Relaunch ; "1": started by HELINK to update itself, so HELINK opens again at the end

Function .onInit
  StrCpy $Relaunch "0"
  ${GetParameters} $R0
  ClearErrors
  ${GetOptions} $R0 "/RELAUNCH" $R1
  IfErrors +2 0
    StrCpy $Relaunch "1"
FunctionEnd
!define MUI_ICON "helink.ico"
!define MUI_UNICON "helink.ico"
!define MUI_ABORTWARNING
!define MUI_WELCOMEFINISHPAGE_BITMAP "welcome.bmp"
!define MUI_UNWELCOMEFINISHPAGE_BITMAP "welcome.bmp"
!define MUI_FINISHPAGE_RUN "$INSTDIR\HELINK.exe"
!define MUI_FINISHPAGE_NOREBOOTSUPPORT
!define MUI_WELCOMEPAGE_TITLE "$(WelcomeTitle)"
!define MUI_WELCOMEPAGE_TEXT "$(WelcomeText)"

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

; Arabic for Arabic Windows, English otherwise.
!insertmacro MUI_LANGUAGE "English"
!insertmacro MUI_LANGUAGE "Arabic"

LangString WelcomeTitle ${LANG_ENGLISH} "HELINK ${VERSION}"
LangString WelcomeTitle ${LANG_ARABIC} "HELINK ${VERSION}"
LangString WelcomeText ${LANG_ENGLISH} "A complete download manager: files and videos, fast and resumable, with an extension for your browser.$\r$\n$\r$\nHELINK installs for your account only and needs no administrator rights. If it is running, it is closed first; its downloads continue when it opens again."
LangString WelcomeText ${LANG_ARABIC} "برنامج تحميل متكامل: الملفات والفيديوهات، بسرعة واستئناف، مع إضافة لمتصفحك.$\r$\n$\r$\nيُثبَّت HELINK لحسابك فقط ولا يحتاج إلى صلاحيات المسؤول. إذا كان يعمل فسيُغلق أولًا، وتُستأنف تنزيلاته عندما يُفتح مرة أخرى."
LangString InUse ${LANG_ENGLISH} "HELINK is still running. Close it from its icon near the clock (right-click, then Exit HELINK), then click Retry."
LangString InUse ${LANG_ARABIC} "ما زال HELINK يعمل. أغلقه من أيقونته بجوار الساعة (اضغطها بالزر الأيمن ثم «إنهاء HELINK»)، ثم اضغط «إعادة المحاولة»."
LangString RemoveData ${LANG_ENGLISH} "Also delete HELINK's settings, its downloads list and its video tools?$\n$\nThe files you downloaded are not deleted."
LangString RemoveData ${LANG_ARABIC} "هل تريد حذف إعدادات HELINK وقائمة التنزيلات وأدوات الفيديو أيضًا؟$\n$\nالملفات التي نزّلتها لن تُحذف."

Section "HELINK"
  SetOutPath "$INSTDIR"
  ; Close the HELINK that runs (installed or portable): the new one asks it, through HELINK's
  ; own channel, and waits. Its downloads pause and continue when it opens again.
  InitPluginsDir
  File "/oname=$PLUGINSDIR\HELINK.exe" "${EXE}"
  ExecWait '"$PLUGINSDIR\HELINK.exe" --quit'

  SetOverwrite try
  StrCpy $R2 0
retry:
  ClearErrors
  File "${EXE}"
  IfErrors 0 copied
    ; HELINK may still be closing (an update it started itself): wait a little, quietly.
    IntOp $R2 $R2 + 1
    IntCmp $R2 30 askuser waitabit askuser
  waitabit:
    Sleep 500
    Goto retry
  askuser:
    MessageBox MB_RETRYCANCEL|MB_ICONEXCLAMATION "$(InUse)" /SD IDCANCEL IDRETRY again
    ; Not updated: the HELINK that asked for the update opens again as it was.
    StrCmp $Relaunch "1" 0 +2
      Exec '"$INSTDIR\HELINK.exe" --updated'
    Abort
  again:
    StrCpy $R2 0
    Goto retry
copied:
  SetOverwrite on

  WriteUninstaller "$INSTDIR\Uninstall HELINK.exe"
  CreateShortcut "$SMPROGRAMS\HELINK.lnk" "$INSTDIR\HELINK.exe"
  ; An update keeps the desktop as the user left it (a deleted shortcut stays deleted).
  IfSilent +2
    CreateShortcut "$DESKTOP\HELINK.lnk" "$INSTDIR\HELINK.exe"

  WriteRegStr HKCU "Software\HELINK" "InstallDir" "$INSTDIR"
  WriteRegStr HKCU "${UNKEY}" "DisplayName" "HELINK"
  WriteRegStr HKCU "${UNKEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "${UNKEY}" "Publisher" "Helal Technology"
  WriteRegStr HKCU "${UNKEY}" "DisplayIcon" "$INSTDIR\HELINK.exe,0"
  WriteRegStr HKCU "${UNKEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNKEY}" "UninstallString" '"$INSTDIR\Uninstall HELINK.exe"'
  WriteRegStr HKCU "${UNKEY}" "QuietUninstallString" '"$INSTDIR\Uninstall HELINK.exe" /S'
  WriteRegDWORD HKCU "${UNKEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNKEY}" "NoRepair" 1
  WriteRegDWORD HKCU "${UNKEY}" "EstimatedSize" 12000

  ; Started by HELINK to update itself: the new HELINK opens.
  StrCmp $Relaunch "1" 0 +2
    Exec '"$INSTDIR\HELINK.exe" --updated'
SectionEnd

Section "Uninstall"
  ; Close HELINK, take it out of Windows' start and of the browsers, remove its helper.
  ExecWait '"$INSTDIR\HELINK.exe" --unregister'
  Delete "$INSTDIR\HELINK.exe"
  Delete "$INSTDIR\Uninstall HELINK.exe"
  RMDir "$INSTDIR"
  Delete "$SMPROGRAMS\HELINK.lnk"
  Delete "$DESKTOP\HELINK.lnk"
  DeleteRegKey HKCU "${UNKEY}"
  DeleteRegKey HKCU "Software\HELINK"
  IfSilent done
  MessageBox MB_YESNO|MB_ICONQUESTION "$(RemoveData)" IDNO done
    RMDir /r "$LOCALAPPDATA\HELINK"
done:
SectionEnd
