; SignalLight per-user installer (Inno Setup 6.3+). Installs to
; %LOCALAPPDATA%\Programs\SignalLight and never needs admin rights.
;
; Local, unsigned build (expects a built windows\signallight.exe):
;   ISCC.exe /DAppVersion=1.2.0 installer\SignalLight.iss
; Release builds are compiled and signed by .github/workflows/release.yml, which
; also passes /DSIGN and defines the "azsign" sign tool on the command line.

#ifndef AppVersion
  #define AppVersion "0.0.0-dev"
#endif

[Setup]
; AppId identifies this app across upgrades. Never change it.
AppId={{5C717BEA-A07D-48A7-A5C0-862393BF941B}
AppName=SignalLight
AppVersion={#AppVersion}
AppVerName=SignalLight {#AppVersion}
AppPublisher=Jarad Duersch
AppPublisherURL=https://github.com/jdtommy/SignalLight
AppSupportURL=https://github.com/jdtommy/SignalLight/issues
AppUpdatesURL=https://github.com/jdtommy/SignalLight/releases
; Per-user install: {autopf} resolves to %LOCALAPPDATA%\Programs, no UAC prompt.
PrivilegesRequired=lowest
DefaultDirName={autopf}\SignalLight
DisableProgramGroupPage=yes
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
UninstallDisplayName=SignalLight
UninstallDisplayIcon={app}\signallight.exe
OutputDir=Output
OutputBaseFilename=SignalLight-Setup-{#AppVersion}
WizardStyle=modern
Compression=lzma2
SolidCompression=yes
#ifdef SIGN
; Signs both Setup and the uninstaller it installs. An unsigned uninstaller could
; be blocked by Smart App Control when the user tries to remove the app.
SignTool=azsign
SignedUninstaller=yes
#endif

[Tasks]
Name: "startup"; Description: "Start SignalLight automatically when I sign in"; GroupDescription: "Startup:"
Name: "desktopicon"; Description: "{cm:CreateDesktopIcon}"; GroupDescription: "{cm:AdditionalIcons}"; Flags: unchecked

[Files]
Source: "..\windows\signallight.exe"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{autoprograms}\SignalLight"; Filename: "{app}\signallight.exe"; WorkingDir: "{app}"
Name: "{autodesktop}\SignalLight"; Filename: "{app}\signallight.exe"; WorkingDir: "{app}"; Tasks: desktopicon

[Registry]
Root: HKCU; Subkey: "Software\Microsoft\Windows\CurrentVersion\Run"; ValueType: string; ValueName: "SignalLight"; ValueData: """{app}\signallight.exe"""; Flags: uninsdeletevalue; Tasks: startup

[Run]
; -from-installer makes the app open the dashboard if no light is paired yet.
Filename: "{app}\signallight.exe"; Parameters: "-from-installer"; Description: "{cm:LaunchProgram,SignalLight}"; Flags: nowait postinstall skipifsilent

[Code]
const
  RunKey = 'Software\Microsoft\Windows\CurrentVersion\Run';

{ Close a running copy so its exe can be replaced or removed. It runs as the
  same user, so no admin rights are needed. }
procedure StopRunningCopy;
var
  ResultCode: Integer;
begin
  Exec(ExpandConstant('{sys}\taskkill.exe'), '/F /IM signallight.exe', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
  Sleep(500);
end;

function PrepareToInstall(var NeedsRestart: Boolean): String;
begin
  StopRunningCopy;
  Result := '';
end;

procedure CurStepChanged(CurStep: TSetupStep);
begin
  { If startup was turned off on an upgrade, remove the entry an earlier install added. }
  if (CurStep = ssPostInstall) and not WizardIsTaskSelected('startup') then
    RegDeleteValue(HKEY_CURRENT_USER, RunKey, 'SignalLight');
end;

function InitializeUninstall(): Boolean;
begin
  StopRunningCopy;
  Result := True;
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
begin
  if (CurUninstallStep = usPostUninstall) and not UninstallSilent then
    if MsgBox('Also remove your SignalLight settings and pairing?' + #13#10#13#10 +
              'Choose No to keep them for a future reinstall. If you remove them, ' +
              'unpair the light (hold its button for 10 seconds) before pairing it again.',
              mbConfirmation, MB_YESNO or MB_DEFBUTTON2) = IDYES then
    begin
      DelTree(ExpandConstant('{userappdata}\SignalLight'), True, True, True);
      DelTree(ExpandConstant('{localappdata}\SignalLight'), True, True, True);
    end;
end;
