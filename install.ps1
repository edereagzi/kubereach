# Installs the latest Kubereach release for the current user, no admin rights needed.
# Usage: irm https://raw.githubusercontent.com/edereagzi/kubereach/main/install.ps1 | iex
$ErrorActionPreference = "Stop"

$dir = "$env:LOCALAPPDATA\Programs\Kubereach"
$zip = "$env:TEMP\kubereach_windows_amd64.zip"

Invoke-WebRequest "https://github.com/edereagzi/kubereach/releases/latest/download/kubereach_windows_amd64.zip" -OutFile $zip
Unblock-File $zip
New-Item -ItemType Directory -Force $dir | Out-Null
Expand-Archive $zip $dir -Force
Remove-Item $zip

$shortcut = (New-Object -ComObject WScript.Shell).CreateShortcut("$env:APPDATA\Microsoft\Windows\Start Menu\Programs\Kubereach.lnk")
$shortcut.TargetPath = "$dir\kubereach.exe"
$shortcut.Save()

$path = [Environment]::GetEnvironmentVariable("Path", "User")
if (($path -split ";") -notcontains $dir) {
    [Environment]::SetEnvironmentVariable("Path", "$path;$dir", "User")
}

Write-Host "Installed $dir\kubereach.exe"
