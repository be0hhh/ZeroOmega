[CmdletBinding()]
param(
    [ValidatePattern('^[a-p]{32}$')]
    [string]$ChromeExtensionId,
    [switch]$NoOpen
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$hostName = 'io.github.zeroomega.wg'
$installDir = Join-Path $env:LOCALAPPDATA 'ZeroOmegaWG'
$executable = Join-Path $installDir 'zeroomega-wg.exe'
$sourceExecutable = Join-Path $PSScriptRoot 'zeroomega-wg.exe'
$utf8 = New-Object System.Text.UTF8Encoding($false)

foreach ($requiredPath in @('zeroomega-wg.exe', 'chrome\manifest.json', 'firefox\zeroomega-wg-unsigned.xpi', 'START-HERE.html', 'uninstall.ps1')) {
    if (-not (Test-Path -LiteralPath (Join-Path $PSScriptRoot $requiredPath) -PathType Leaf)) {
        throw "Missing $requiredPath. Use the complete Windows installer or kit."
    }
}
if (-not $ChromeExtensionId) {
    $idFile = Join-Path $PSScriptRoot 'chrome-extension-id.txt'
    if (-not (Test-Path -LiteralPath $idFile -PathType Leaf)) {
        throw 'chrome-extension-id.txt is missing. Pass -ChromeExtensionId explicitly.'
    }
    $ChromeExtensionId = (Get-Content -LiteralPath $idFile -Raw).Trim()
    if ($ChromeExtensionId -notmatch '^[a-p]{32}$') {
        throw 'Invalid Chrome extension ID in chrome-extension-id.txt.'
    }
}

# Only our current-user broker is asked to stop; do not kill processes by name.
if (Test-Path -LiteralPath $executable -PathType Leaf) {
    $stop = Start-Process -FilePath $executable -ArgumentList '--shutdown' -Wait -PassThru -WindowStyle Hidden
    if ($stop.ExitCode -ne 0) {
        throw 'Could not stop the existing companion. Close the browsers and retry.'
    }
}
New-Item -ItemType Directory -Path $installDir -Force | Out-Null
if ([IO.Path]::GetFullPath($sourceExecutable) -ne [IO.Path]::GetFullPath($executable)) {
    try {
        Copy-Item -LiteralPath $sourceExecutable -Destination $executable -Force
    } catch {
        throw 'Could not update the companion file. Close Chrome and Firefox completely, then run the installer again.'
    }
}
foreach ($folderName in @('chrome', 'firefox', 'source')) {
    $sourceFolder = Join-Path $PSScriptRoot $folderName
    $destinationFolder = Join-Path $installDir $folderName
    if (Test-Path -LiteralPath $sourceFolder -PathType Container) {
        New-Item -ItemType Directory -Path $destinationFolder -Force | Out-Null
        Get-ChildItem -LiteralPath $sourceFolder -Force | ForEach-Object {
            Copy-Item -LiteralPath $_.FullName -Destination $destinationFolder -Recurse -Force
        }
    }
}
foreach ($fileName in @('COPYING', 'AUTHORS', 'Go-LICENSE.txt', 'THIRD_PARTY_NOTICES.txt', 'BUILD-INFO.json', 'README.ru.md', 'README.en.md', 'chrome-extension-id.txt')) {
    $sourceFile = Join-Path $PSScriptRoot $fileName
    if (Test-Path -LiteralPath $sourceFile -PathType Leaf) {
        Copy-Item -LiteralPath $sourceFile -Destination (Join-Path $installDir $fileName) -Force
    }
}
$chromeDir = Join-Path $installDir 'chrome'
$helpPath = Join-Path $installDir 'START-HERE.html'
$helpHtml = [IO.File]::ReadAllText((Join-Path $PSScriptRoot 'START-HERE.html'))
$helpHtml = $helpHtml.Replace('{{CHROME_PATH}}', [System.Net.WebUtility]::HtmlEncode($chromeDir))
[IO.File]::WriteAllText($helpPath, $helpHtml, $utf8)

$chromeManifestPath = Join-Path $installDir 'native-host-chrome.json'
$firefoxManifestPath = Join-Path $installDir 'native-host-firefox.json'
$chromeManifest = [ordered]@{
    name = $hostName
    description = 'ZeroOmega WireGuard and AmneziaWG companion'
    path = $executable
    type = 'stdio'
    allowed_origins = @("chrome-extension://$ChromeExtensionId/")
}
$firefoxManifest = [ordered]@{
    name = $hostName
    description = 'ZeroOmega WireGuard and AmneziaWG companion'
    path = $executable
    type = 'stdio'
    allowed_extensions = @('zeroomega-wg@local.invalid')
}
[IO.File]::WriteAllText($chromeManifestPath, ($chromeManifest | ConvertTo-Json -Depth 4), $utf8)
[IO.File]::WriteAllText($firefoxManifestPath, ($firefoxManifest | ConvertTo-Json -Depth 4), $utf8)
foreach ($entry in @(
    @{ Key = "HKCU:\Software\Google\Chrome\NativeMessagingHosts\$hostName"; Manifest = $chromeManifestPath },
    @{ Key = "HKCU:\Software\Mozilla\NativeMessagingHosts\$hostName"; Manifest = $firefoxManifestPath }
)) {
    New-Item -Path $entry.Key -Force | Out-Null
    Set-Item -LiteralPath $entry.Key -Value $entry.Manifest
}
Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'uninstall.ps1') -Destination $installDir -Force
$shortcutDir = Join-Path $env:APPDATA 'Microsoft\Windows\Start Menu\Programs'
New-Item -ItemType Directory -Path $shortcutDir -Force | Out-Null
$shortcutPath = Join-Path $shortcutDir 'ZeroOmega WG.lnk'
$shell = New-Object -ComObject WScript.Shell
$shortcut = $shell.CreateShortcut($shortcutPath)
$shortcut.TargetPath = $helpPath
$shortcut.WorkingDirectory = $installDir
$shortcut.Description = 'ZeroOmega WG - browser setup and help'
$shortcut.Save()

$healthInfo = New-Object System.Diagnostics.ProcessStartInfo
$healthInfo.FileName = $executable
$healthInfo.Arguments = '--start'
$healthInfo.WorkingDirectory = $installDir
$healthInfo.UseShellExecute = $false
$healthInfo.CreateNoWindow = $true
$start = [System.Diagnostics.Process]::Start($healthInfo)
try {
    # Wait only for the health-check process, not its long-lived broker child.
    if (-not $start.WaitForExit(30000)) {
        $start.Kill()
        throw 'Companion startup timed out. Close browsers and run the installer again.'
    }
    if ($start.ExitCode -ne 0) {
        throw 'Files were installed, but the companion health check failed. Close browsers and run the installer again.'
    }
} finally {
    $start.Dispose()
}
Write-Host "Installed ZeroOmega WG companion for the current Windows user in $installDir"
Write-Host "Chrome extension ID: $ChromeExtensionId"
Write-Host "Companion started and healthy. Chrome extension folder: $chromeDir"
Write-Host 'The downloaded installer may now be deleted. Keep the installed application folder.'
Write-Host 'Firefox permanent installation still requires a Mozilla-signed XPI.'
if (-not $NoOpen) {
    Start-Process -FilePath $helpPath
    Start-Process -FilePath 'explorer.exe' -ArgumentList ('"' + $chromeDir + '"')
    $chromeCandidates = @(
        (Join-Path $env:LOCALAPPDATA 'Google\Chrome\Application\chrome.exe'),
        (Join-Path $env:ProgramFiles 'Google\Chrome\Application\chrome.exe')
    )
    if (${env:ProgramFiles(x86)}) {
        $chromeCandidates += Join-Path ${env:ProgramFiles(x86)} 'Google\Chrome\Application\chrome.exe'
    }
    foreach ($chromePath in $chromeCandidates) {
        if (Test-Path -LiteralPath $chromePath -PathType Leaf) {
            Start-Process -FilePath $chromePath -ArgumentList 'chrome://extensions'
            break
        }
    }
}
