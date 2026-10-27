; PowerDeck for Windows installer (Inno Setup 6). Built by the release workflow:
;   iscc /DAppVersion=1.2.3 /DSourceExe=..\dist\PowerDeck.exe installer\PowerDeck.iss
; One click: no wizard pages. Double-click, approve the UAC prompt, and PowerDeck opens to pair a phone.
; Silent mode (/VERYSILENT) is what automatic updates use.

#ifndef AppVersion
  #define AppVersion "0.0.0"
#endif
#ifndef SourceExe
  #define SourceExe "..\dist\PowerDeck.exe"
#endif

[Setup]
AppId={{AA51379F-AFFD-4719-97F5-8459A3C38190}
AppName=PowerDeck
AppVersion={#AppVersion}
AppVerName=PowerDeck {#AppVersion}
AppPublisher=PowerDeck
AppPublisherURL=https://github.com/ProjectZero02/powerdeck
AppSupportURL=https://github.com/ProjectZero02/powerdeck/issues
AppUpdatesURL=https://github.com/ProjectZero02/powerdeck/releases
VersionInfoVersion={#AppVersion}
DefaultDirName={autopf}\PowerDeck
UninstallDisplayName=PowerDeck
UninstallDisplayIcon={app}\PowerDeck.exe
SetupIconFile=..\assets\powerdeck.ico
WizardStyle=modern
DisableWelcomePage=yes
DisableDirPage=yes
DisableProgramGroupPage=yes
DisableReadyPage=yes
DisableFinishedPage=yes
PrivilegesRequired=admin
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
MinVersion=10.0
CloseApplications=no
OutputDir=..\dist
OutputBaseFilename=PowerDeck-Setup
Compression=lzma2/max
SolidCompression=yes

[Files]
Source: "{#SourceExe}"; DestDir: "{app}"; DestName: "PowerDeck.exe"; Flags: ignoreversion

[Icons]
Name: "{autoprograms}\PowerDeck"; Filename: "{app}\PowerDeck.exe"; Parameters: "open"; Comment: "Pair phones and see PowerDeck's status"

[Run]
Filename: "{app}\PowerDeck.exe"; Parameters: "open"; Flags: nowait postinstall runasoriginaluser; Check: not WizardSilent

[UninstallRun]
Filename: "{app}\PowerDeck.exe"; Parameters: "uninstall"; Flags: runhidden waituntilterminated; RunOnceId: "PowerDeckUninstall"

[UninstallDelete]
Type: filesandordirs; Name: "{commonappdata}\PowerDeck"

[Code]
// Stop the running service so its executable can be replaced (upgrades and automatic updates).
function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  ResultCode: Integer;
begin
  Exec(ExpandConstant('{sys}\net.exe'), 'stop PowerDeck', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
  Result := '';
end;

// Register the service, firewall rule and Wake-on-LAN settings, then start the service.
procedure CurStepChanged(CurStep: TSetupStep);
var
  ResultCode: Integer;
begin
  if CurStep = ssPostInstall then
  begin
    WizardForm.StatusLabel.Caption := 'Setting up PowerDeck (service, firewall, Wake-on-LAN)...';
    if not Exec(ExpandConstant('{app}\PowerDeck.exe'), 'install', '', SW_HIDE, ewWaitUntilTerminated, ResultCode) or (ResultCode <> 0) then
      SuppressibleMsgBox('PowerDeck was installed but could not finish setting itself up.' + #13#10 + #13#10 +
        'Details are in ' + ExpandConstant('{tmp}\..\PowerDeck-install.log') + '. Running the installer again usually fixes it.',
        mbError, MB_OK, IDOK);
  end;
end;
