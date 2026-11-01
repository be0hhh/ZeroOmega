[CmdletBinding()]
param([switch]$RemoveConfigs)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$hostName = 'io.github.zeroomega.wg'
$installDir = Join-Path $env:LOCALAPPDATA 'ZeroOmegaWG'
$executable = Join-Path $installDir 'zeroomega-wg.exe'

if (Test-Path -LiteralPath $executable -PathType Leaf) {
    $stop = Start-Process -FilePath $executable -ArgumentList '--shutdown' -Wait -PassThru -WindowStyle Hidden
    if ($stop.ExitCode -ne 0) {
        throw 'Could not stop the companion. Close the browsers and retry; no files were removed.'
    }
}
foreach ($registryKey in @(
    "HKCU:\Software\Google\Chrome\NativeMessagingHosts\$hostName",
    "HKCU:\Software\Mozilla\NativeMessagingHosts\$hostName"
)) {
    # Do not remove a registration replaced by a different installation.
    if (Test-Path -LiteralPath $registryKey) {
        $manifest = (Get-Item -LiteralPath $registryKey).GetValue('')
        if ($manifest -and ([IO.Path]::GetDirectoryName($manifest) -eq $installDir)) {
            Remove-Item -LiteralPath $registryKey -Recurse -Force
        }
    }
}
foreach ($name in @('zeroomega-wg.exe', 'native-host-chrome.json', 'native-host-firefox.json', 'uninstall.ps1', 'START-HERE.html', 'COPYING', 'AUTHORS', 'Go-LICENSE.txt', 'THIRD_PARTY_NOTICES.txt', 'BUILD-INFO.json', 'README.ru.md', 'README.en.md', 'chrome-extension-id.txt', 'setup.log')) {
    $path = Join-Path $installDir $name
    if (Test-Path -LiteralPath $path -PathType Leaf) {
        Remove-Item -LiteralPath $path -Force
    }
}
foreach ($name in @('chrome', 'firefox', 'source')) {
    $path = Join-Path $installDir $name
    if (Test-Path -LiteralPath $path -PathType Container) {
        Remove-Item -LiteralPath $path -Recurse -Force
    }
}
$shortcutPath = Join-Path $env:APPDATA 'Microsoft\Windows\Start Menu\Programs\ZeroOmega WG.lnk'
if (Test-Path -LiteralPath $shortcutPath -PathType Leaf) {
    $shell = New-Object -ComObject WScript.Shell
    $shortcut = $shell.CreateShortcut($shortcutPath)
    if ($shortcut.TargetPath -eq (Join-Path $installDir 'START-HERE.html')) {
        Remove-Item -LiteralPath $shortcutPath -Force
    }
}
if ($RemoveConfigs) {
    $configsDir = Join-Path $installDir 'configs'
    if (Test-Path -LiteralPath $configsDir) {
        Remove-Item -LiteralPath $configsDir -Recurse -Force
    }
    Write-Host 'Companion removed, including its saved configurations.'
} else {
    Write-Host 'Companion removed. Saved configurations were preserved for this Windows user.'
    Write-Host 'Use -RemoveConfigs to also remove the saved configurations.'
}
Write-Host 'Remove the browser extension separately. After it is disabled or removed, browser routing returns to normal.'
