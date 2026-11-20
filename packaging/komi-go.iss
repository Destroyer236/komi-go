; Komi Go per-user installer. Build with:
;   ISCC.exe /DMyAppVersion=0.3.0 /DMyArch=amd64 packaging\komi-go.iss
; or simply: .\scripts\build.ps1 -Installer

#ifndef MyAppVersion
  #define MyAppVersion "0.3.0"
#endif
#ifndef MyArch
  #define MyArch "amd64"
#endif

[Setup]
AppId={{8E4C0B1A-5D7E-4A2B-9C31-6F0D2A7B8E44}
AppName=Komi Go
AppVersion={#MyAppVersion}
AppPublisher=Komi Go
VersionInfoVersion={#MyAppVersion}.0
VersionInfoProductVersion={#MyAppVersion}.0
VersionInfoCompany=Komi Go
VersionInfoProductName=Komi Go
VersionInfoDescription=Komi Go 安装程序
VersionInfoCopyright=Apache-2.0
AppPublisherURL=https://github.com/
AppSupportURL=https://github.com/
AppUpdatesURL=https://github.com/
DefaultDirName={localappdata}\Programs\Komi Go
DefaultGroupName=Komi Go
DisableProgramGroupPage=yes
PrivilegesRequired=lowest
OutputDir=..\dist
OutputBaseFilename=KomiGo-{#MyAppVersion}-{#MyArch}-setup
SetupIconFile=..\build\windows\icon.ico
UninstallDisplayIcon={app}\komi-go.exe
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
CloseApplications=yes
RestartApplications=no
; WebView2 Evergreen Runtime is required. Win11 ships it; Win10 users can
; install it from Microsoft. The app shows a clear error if it is missing.

#ifdef MyArchIsArm64
ArchitecturesAllowed=arm64
#else
ArchitecturesAllowed=x64compatible
#endif

[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"

[Tasks]
Name: "desktopicon"; Description: "创建桌面快捷方式"; GroupDescription: "附加图标:"; Flags: unchecked
Name: "autostart"; Description: "登录 Windows 时后台启动（--tray）"; GroupDescription: "启动:"; Flags: unchecked

[Files]
Source: "..\dist\KomiGo-{#MyAppVersion}-{#MyArch}.exe"; DestDir: "{app}"; DestName: "komi-go.exe"; Flags: ignoreversion

[Icons]
Name: "{group}\Komi Go"; Filename: "{app}\komi-go.exe"
Name: "{group}\卸载 Komi Go"; Filename: "{uninstallexe}"
Name: "{userdesktop}\Komi Go"; Filename: "{app}\komi-go.exe"; Tasks: desktopicon
Name: "{userstartup}\Komi Go"; Filename: "{app}\komi-go.exe"; Parameters: "--tray"; Tasks: autostart

[Registry]
; Register komi:// before the first launch so share links work immediately.
Root: HKCU; Subkey: "Software\Classes\komi"; ValueType: string; ValueName: ""; ValueData: "URL:Komi Go Protocol"; Flags: uninsdeletekey
Root: HKCU; Subkey: "Software\Classes\komi"; ValueType: string; ValueName: "URL Protocol"; ValueData: ""; Flags: uninsdeletevalue
Root: HKCU; Subkey: "Software\Classes\komi\DefaultIcon"; ValueType: string; ValueName: ""; ValueData: "{app}\komi-go.exe,0"; Flags: uninsdeletekey
Root: HKCU; Subkey: "Software\Classes\komi\shell\open\command"; ValueType: string; ValueName: ""; ValueData: """{app}\komi-go.exe"" ""%1"""; Flags: uninsdeletekey

[Run]
Filename: "{app}\komi-go.exe"; Description: "启动 Komi Go"; Flags: nowait postinstall skipifsilent
