param(
    [string]$Version = "dev"
)

$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $PSScriptRoot
$Dist = Join-Path $Root "dist"
$Output = Join-Path $Dist "GrokGateway.exe"

Push-Location $Root
try {
    go test ./...
    go vet ./...
    New-Item -ItemType Directory -Force -Path $Dist | Out-Null
    $env:CGO_ENABLED = "0"
    $env:GOOS = "windows"
    $env:GOARCH = "amd64"
    go build -tags production -trimpath -ldflags "-H windowsgui -s -w -X main.version=$Version" -o $Output ./cmd/grok-gateway
    Copy-Item README.md, config.example.json, THIRD_PARTY_NOTICES.md -Destination $Dist -Force
    Copy-Item docs\DEVELOPMENT.md -Destination $Dist -Force
    Write-Host "Built: $Output"
}
finally {
    Pop-Location
}
