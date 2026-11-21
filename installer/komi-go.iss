; Komi Go — Inno Setup 6 script.
;
; Build it through scripts\build-installer.ps1, which passes the version, the
; compiled exe, the repository root and the Chinese language file as /D defines.
; Everything user-visible here is Chinese; the wizard falls back to English when
; the .isl cannot be loaded.

#define AppName "Komi Go"
#define AppPublisher "Komi Go"
#define AppURL "https://github.com/Destroyer236/komi-go"

#ifndef AppVersion
  #define AppVersion "0.3.2"
#endif
#ifndef SourceExe
  #define SourceExe "..\dist\KomiGo-0.3.2-amd64.exe"
#endif
#ifndef SourceDir
  #define SourceDir ".."
#endif
#ifndef LicenseFile
  #define LicenseFile "..\LICENSE"
#endif
#ifndef ChineseIsl
  #define ChineseIsl "ChineseSimplified.isl"
#endif

[Setup]
; A fixed AppId keeps upgrades in place instead of installing a second copy.
AppId={{8E4C0B1A-5D7E-4A2B-9C31-6F0D2A7B8E44}
AppName={#AppName}
AppVersion={#AppVersion}
AppVerName={#AppName} {#AppVersion}
AppPublisher={#AppPublisher}
AppPublisherURL={#AppURL}
AppSupportURL={#AppURL}/issues
AppUpdatesURL={#AppURL}/releases
VersionInfoVersion={#AppVersion}.0
VersionInfoProductName={#AppName}
VersionInfoCompany={#AppPublisher}
VersionInfoDescription={#AppName} installer
DefaultDirName={autopf}\Komi Go
DefaultGroupName=Komi Go
DisableProgramGroupPage=yes
AllowNoIcons=yes
; The app itself works without administrator rights; MSI installs raise their
; own UAC prompt. An administrator can still choose an all-users install.
PrivilegesRequired=lowest
PrivilegesRequiredOverridesAllowed=dialog commandline
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
MinVersion=10.0
OutputDir={#SourceDir}\dist
OutputBaseFilename=KomiGo-{#AppVersion}-amd64-setup
SetupIconFile={#SourceDir}\build\windows\icon.ico
UninstallDisplayIcon={app}\komi-go.exe
UninstallDisplayName={#AppName} {#AppVersion}
Compression=lzma2/max
SolidCompression=yes
WizardStyle=modern
; Let Restart Manager ask before replacing the running exe during an upgrade.
CloseApplications=yes
RestartApplications=no
ShowLanguageDialog=auto

[Languages]
Name: "zhcn"; MessagesFile: "{#ChineseIsl}"
Name: "en"; MessagesFile: "compiler:Default.isl"

[Tasks]
Name: "desktopicon"; Description: "创建桌面快捷方式"; GroupDescription: "附加任务："; Flags: unchecked

[Files]
Source: "{#SourceExe}"; DestDir: "{app}"; DestName: "komi-go.exe"; Flags: ignoreversion
Source: "{#LicenseFile}"; DestDir: "{app}"; DestName: "LICENSE.txt"; Flags: ignoreversion

[Icons]
Name: "{autoprograms}\Komi Go"; Filename: "{app}\komi-go.exe"; Comment: "GitHub Release 应用商店"
Name: "{autoprograms}\卸载 Komi Go"; Filename: "{uninstallexe}"
Name: "{autodesktop}\Komi Go"; Filename: "{app}\komi-go.exe"; Tasks: desktopicon

[Run]
Filename: "{app}\komi-go.exe"; Description: "启动 Komi Go"; Flags: nowait postinstall skipifsilent
