!ifndef VERSION
  !error "VERSION is required"
!endif
!ifndef ARCH
  !error "ARCH is required"
!endif
!ifndef SOURCE
  !error "SOURCE is required"
!endif
!ifndef OUTPUT
  !error "OUTPUT is required"
!endif

Unicode True
RequestExecutionLevel user
SetCompressor /SOLID lzma

!include "MUI2.nsh"

!define PRODUCT_NAME "Chronicle Sidekick"
!define PRODUCT_PUBLISHER "Chronicle"
!define PRODUCT_WEB_SITE "https://chronicleclassic.com"
!define PRODUCT_REG_KEY "Software\Chronicle Sidekick"
!define PRODUCT_UNINSTALL_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\Chronicle Sidekick"
!define PRODUCT_RUN_KEY "Software\Microsoft\Windows\CurrentVersion\Run"
!define PRODUCT_RUN_VALUE "com.chronicle.uploader"

Name "${PRODUCT_NAME} ${VERSION}"
OutFile "${OUTPUT}"
InstallDir "$LOCALAPPDATA\Programs\Chronicle Sidekick"
InstallDirRegKey HKCU "${PRODUCT_REG_KEY}" "InstallDir"
BrandingText "${PRODUCT_NAME}"

!define MUI_ABORTWARNING
!define MUI_FINISHPAGE_RUN "$INSTDIR\chronicle-sidekick.exe"
!define MUI_FINISHPAGE_RUN_TEXT "Start Chronicle Sidekick"

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "English"

Section "Chronicle Sidekick" SectionMain
  SectionIn RO
  SetShellVarContext current
  SetOutPath "$INSTDIR"
  File /oname=chronicle-sidekick.exe "${SOURCE}"
  WriteUninstaller "$INSTDIR\Uninstall.exe"

  CreateDirectory "$SMPROGRAMS\Chronicle Sidekick"
  CreateShortcut "$SMPROGRAMS\Chronicle Sidekick\Chronicle Sidekick.lnk" "$INSTDIR\chronicle-sidekick.exe"
  CreateShortcut "$SMPROGRAMS\Chronicle Sidekick\Uninstall Chronicle Sidekick.lnk" "$INSTDIR\Uninstall.exe"

  WriteRegStr HKCU "${PRODUCT_REG_KEY}" "InstallDir" "$INSTDIR"
  WriteRegStr HKCU "${PRODUCT_UNINSTALL_KEY}" "DisplayName" "${PRODUCT_NAME}"
  WriteRegStr HKCU "${PRODUCT_UNINSTALL_KEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "${PRODUCT_UNINSTALL_KEY}" "Publisher" "${PRODUCT_PUBLISHER}"
  WriteRegStr HKCU "${PRODUCT_UNINSTALL_KEY}" "URLInfoAbout" "${PRODUCT_WEB_SITE}"
  WriteRegStr HKCU "${PRODUCT_UNINSTALL_KEY}" "DisplayIcon" "$INSTDIR\chronicle-sidekick.exe"
  WriteRegStr HKCU "${PRODUCT_UNINSTALL_KEY}" "UninstallString" '"$INSTDIR\Uninstall.exe"'
  WriteRegDWORD HKCU "${PRODUCT_UNINSTALL_KEY}" "NoModify" 1
  WriteRegDWORD HKCU "${PRODUCT_UNINSTALL_KEY}" "NoRepair" 1
SectionEnd

Section "Uninstall"
  SetShellVarContext current
  DeleteRegValue HKCU "${PRODUCT_RUN_KEY}" "${PRODUCT_RUN_VALUE}"
  DeleteRegKey HKCU "${PRODUCT_UNINSTALL_KEY}"
  DeleteRegKey HKCU "${PRODUCT_REG_KEY}"

  Delete "$SMPROGRAMS\Chronicle Sidekick\Chronicle Sidekick.lnk"
  Delete "$SMPROGRAMS\Chronicle Sidekick\Uninstall Chronicle Sidekick.lnk"
  RMDir "$SMPROGRAMS\Chronicle Sidekick"

  Delete "$INSTDIR\chronicle-sidekick.exe"
  Delete "$INSTDIR\Uninstall.exe"
  RMDir "$INSTDIR"
SectionEnd
