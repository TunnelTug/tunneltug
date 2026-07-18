# Publish 0TrustCloud modules and selected apps (excludes ack/motion/williwaw).
# Requires: git, go, GitHub token via git-credential-manager or $env:GITHUB_TOKEN
$ErrorActionPreference = "Stop"
$env:Path = "C:\Program Files\Git\cmd;C:\Program Files\Git\bin;C:\Program Files\Go\bin;" + $env:Path

if (-not $env:GITHUB_TOKEN) {
  $credIn = "protocol=https`nhost=github.com`n`n"
  $credOut = $credIn | & "C:\Program Files\Git\mingw64\bin\git-credential-manager.exe" get 2>$null
  $env:GITHUB_TOKEN = ($credOut | Where-Object { $_ -match '^password=' }) -replace '^password=',''
}
$tok = $env:GITHUB_TOKEN
if (-not $tok) { throw "No GITHUB_TOKEN available" }

$env:GOPROXY = "direct"
$env:GOSUMDB = "off"
$env:GIT_TERMINAL_PROMPT = "0"

$base = "C:\Users\grego\0TrustCloud"
$work = "C:\Users\grego\tunneltug\.publish-0trust"
$logFile = Join-Path $work "publish.log"
if (Test-Path $work) { Remove-Item -Recurse -Force $work }
New-Item -ItemType Directory -Path $work | Out-Null

function Log($msg) {
  $line = "[{0}] {1}" -f (Get-Date -Format "HH:mm:ss"), $msg
  Write-Host $line
  Add-Content -Path $logFile -Value $line
}

$headers = @{
  "User-Agent"           = "0trust-publish"
  "Authorization"        = "Bearer $tok"
  "Accept"               = "application/vnd.github+json"
  "X-GitHub-Api-Version" = "2022-11-28"
  "Content-Type"         = "application/json"
}

function Ensure-Repo([string]$Name, [string]$Description) {
  try {
    $null = Invoke-RestMethod -Uri "https://api.github.com/repos/0TrustCloud/$Name" -Headers $headers
    Log "repo exists: $Name"
    return
  } catch {}
  $body = @{
    name        = $Name
    description = $Description
    private     = $false
    auto_init   = $false
    has_issues  = $true
    has_projects = $false
    has_wiki    = $false
  } | ConvertTo-Json
  try {
    $r = Invoke-RestMethod -Method Post -Uri "https://api.github.com/orgs/0TrustCloud/repos" -Headers $headers -Body $body
    Log "CREATED $($r.full_name)"
  } catch {
    $msg = $_.ErrorDetails.Message
    if (-not $msg) { $msg = $_.Exception.Message }
    if ($msg -match 'already exists|name already exists') {
      Log "repo already exists (race): $Name"
    } else {
      throw "Failed creating $Name : $msg"
    }
  }
}

function Strip-Replaces([string]$path) {
  if (-not (Test-Path $path)) { return }
  $lines = Get-Content $path
  $out = New-Object System.Collections.Generic.List[string]
  $i = 0
  while ($i -lt $lines.Count) {
    $line = $lines[$i]
    if ($line -match '^\s*replace\s+') {
      if ($line -match '=>') { $i++; continue }
      if ($line -match 'replace\s*\(') {
        $i++
        while ($i -lt $lines.Count -and $lines[$i] -notmatch '^\s*\)\s*$') { $i++ }
        $i++; continue
      }
      $i++; continue
    }
    $out.Add($line)
    $i++
  }
  while ($out.Count -gt 0 -and $out[$out.Count - 1] -match '^\s*$') { $out.RemoveAt($out.Count - 1) }
  ($out -join "`n") + "`n" | Set-Content -Path $path -NoNewline -Encoding utf8
}

function Apply-Pins([string]$path, [hashtable]$pins) {
  if (-not (Test-Path $path) -or $pins.Count -eq 0) { return }
  $mod = Get-Content $path -Raw
  foreach ($k in $pins.Keys) {
    $ver = $pins[$k]
    $mod = [regex]::Replace($mod, "github.com/0TrustCloud/$([regex]::Escape($k)) v[\w\.\-+]+", "github.com/0TrustCloud/$k $ver")
  }
  # common v0.0.0 leftovers for known modules
  foreach ($k in $pins.Keys) {
    $ver = $pins[$k]
    $mod = $mod -replace "github.com/0TrustCloud/$([regex]::Escape($k)) v0\.0\.0", "github.com/0TrustCloud/$k $ver"
  }
  Set-Content -Path $path -Value $mod -NoNewline -Encoding utf8
}

function Ensure-Gitignore([string]$dst) {
  $gi = Join-Path $dst ".gitignore"
  if (-not (Test-Path $gi)) {
    @"
*.exe
*.test
*.out
/bin/
.DS_Store
.env
.env.*
platform.exe
services.exe
"@ | Set-Content $gi -Encoding utf8
  }
}

function Publish-Package {
  param(
    [string]$Name,
    [string]$Tag,
    [string]$SrcPath,   # absolute source directory
    [hashtable]$Pins = @{},
    [switch]$SkipBuild
  )
  Log "======== $Name @ $Tag ========"
  if (-not (Test-Path $SrcPath)) { throw "Missing source $SrcPath" }

  $dst = Join-Path $work $Name
  $auth = "https://x-access-token:${tok}@github.com/0TrustCloud/$Name.git"

  if (Test-Path $dst) { Remove-Item -Recurse -Force $dst }

  $heads = git ls-remote $auth HEAD 2>$null
  if ($heads) {
    git clone --depth 30 $auth $dst 2>&1 | Out-Null
  } else {
    New-Item -ItemType Directory -Path $dst | Out-Null
    Push-Location $dst
    git init -b main | Out-Null
    git remote add origin $auth
    Pop-Location
  }

  Push-Location $dst
  try {
    git checkout main 2>$null | Out-Null
    Get-ChildItem -Force | Where-Object { $_.Name -ne '.git' } | Remove-Item -Recurse -Force
    Get-ChildItem $SrcPath -Force | Where-Object { $_.Name -notin @('.git', 'bin') -and $_.Name -notmatch '\.exe$' } | ForEach-Object {
      Copy-Item $_.FullName (Join-Path $dst $_.Name) -Recurse -Force
    }

    $gomod = Join-Path $dst "go.mod"
    if (Test-Path $gomod) {
      Strip-Replaces $gomod
      Apply-Pins $gomod $Pins
      go mod tidy 2>&1 | ForEach-Object { Log "  tidy: $_" }
      if ($LASTEXITCODE -ne 0) { throw "go mod tidy failed for $Name" }
      if (-not $SkipBuild) {
        go build ./... 2>&1 | ForEach-Object { Log "  build: $_" }
        if ($LASTEXITCODE -ne 0) { throw "go build failed for $Name" }
      }
    }

    Ensure-Gitignore $dst
    git config user.name "gddisney"
    git config user.email "gregory.disney@owasp.org"
    git add -A
    if (git status --porcelain) {
      git commit -m "Publish $Name $Tag" | Out-Null
      git push -u origin HEAD:main 2>&1 | ForEach-Object { Log "  push: $_" }
      if ($LASTEXITCODE -ne 0) { throw "git push failed for $Name" }
    } else {
      Log "  no file changes; ensuring branch/tag"
      git push -u origin HEAD:main 2>$null | Out-Null
    }

    $existing = git ls-remote --tags origin "refs/tags/$Tag" 2>$null
    if ($existing) {
      Log "  tag $Tag already exists"
    } else {
      git tag -d $Tag 2>$null | Out-Null
      git tag -a $Tag -m "Release $Tag"
      git push origin $Tag 2>&1 | ForEach-Object { Log "  tag: $_" }
      if ($LASTEXITCODE -ne 0) { throw "tag push failed for $Name $Tag" }
      Log "  tagged $Tag"
    }
    Log "OK $Name $Tag"
  } finally {
    Pop-Location
  }
}

# ---- Latest pins for 0TrustCloud modules (updated as we publish) ----
$pins = @{
  ultimate_db          = "v1.3.6"
  logger               = "v1.0.3"
  guikit               = "v1.2.2"
  secure_data_format   = "v1.0.0"
  secure_policy        = "v1.0.6"
  samln                = "v1.0.5"
  auth_provider        = "v1.0.5"
  secure_network       = "v1.1.6"
  secure_ssh           = "v0.1.2"
  secure_k8s           = "v0.1.4"
  mesh_client          = "v0.1.4"
  secure_dns           = "v1.0.3"
  secure_registrar     = "v1.0.1"
  service_keys         = "v1.0.0"
  orchid_sync          = "v1.0.4"
  identity_provider    = "v1.0.0"
  webauthnext          = "v1.0.6"
  secure_bootstrap     = "v1.0.0"
  secure_boilerplate   = "v1.0.0"
  ultimate_keystore    = "v1.0.0"
  # new / to-publish
  orchid_log           = "v0.1.0"
  mailer               = "v0.1.0"
  gifsearch            = "v0.1.0"
  product_pwa          = "v0.1.0"
  product_otrust       = "v0.1.0"
  product_security     = "v0.1.0"
  socialcdn            = "v0.1.0"
  vpn                  = "v0.1.0"
  moderation           = "v0.1.0"
}

# ---- Create missing repos ----
$repos = @(
  @{n='gifsearch'; d='0TrustCloud gifsearch module'},
  @{n='mailer'; d='0TrustCloud mailer module'},
  @{n='moderation'; d='0TrustCloud moderation module'},
  @{n='orchid_log'; d='0TrustCloud orchid_log module'},
  @{n='product_otrust'; d='0TrustCloud product_otrust module'},
  @{n='product_pwa'; d='0TrustCloud product_pwa module'},
  @{n='product_security'; d='0TrustCloud product_security module'},
  @{n='socialcdn'; d='0TrustCloud social CDN module'},
  @{n='vpn'; d='0TrustCloud VPN module'},
  @{n='platform'; d='0TrustCloud platform control plane'},
  @{n='services'; d='0TrustCloud services / mesh plane'},
  @{n='mail'; d='0TrustCloud mail service'},
  @{n='search'; d='0TrustCloud search service'},
  @{n='social'; d='0trust.social application'},
  @{n='0trust'; d='0TrustCloud CLI client'}
)
foreach ($r in $repos) { Ensure-Repo $r.n $r.d }

# ---- Publish leaf modules (no/few 0trust deps) ----
Publish-Package -Name orchid_log -Tag $pins.orchid_log -SrcPath "$base\modules\orchid_log" -Pins $pins
Publish-Package -Name mailer -Tag $pins.mailer -SrcPath "$base\modules\mailer" -Pins $pins
Publish-Package -Name gifsearch -Tag $pins.gifsearch -SrcPath "$base\modules\gifsearch" -Pins $pins
Publish-Package -Name product_pwa -Tag $pins.product_pwa -SrcPath "$base\modules\product_pwa" -Pins $pins
Publish-Package -Name product_otrust -Tag $pins.product_otrust -SrcPath "$base\modules\product_otrust" -Pins $pins
Publish-Package -Name product_security -Tag $pins.product_security -SrcPath "$base\modules\product_security" -Pins $pins
Publish-Package -Name socialcdn -Tag $pins.socialcdn -SrcPath "$base\modules\socialcdn" -Pins $pins
Publish-Package -Name vpn -Tag $pins.vpn -SrcPath "$base\modules\vpn" -Pins $pins

# ---- Intermediate: moderation, ultimate_keystore, refresh guikit/data_format if needed ----
Publish-Package -Name moderation -Tag $pins.moderation -SrcPath "$base\modules\moderation" -Pins $pins
Publish-Package -Name ultimate_keystore -Tag "v1.0.1" -SrcPath "$base\modules\ultimate_keystore" -Pins $pins
$pins.ultimate_keystore = "v1.0.1"

# Refresh core modules that platform needs at published versions
Publish-Package -Name service_keys -Tag "v1.0.1" -SrcPath "$base\modules\service_keys" -Pins $pins
$pins.service_keys = "v1.0.1"
Publish-Package -Name guikit -Tag "v1.2.3" -SrcPath "$base\modules\guikit" -Pins $pins
$pins.guikit = "v1.2.3"
Publish-Package -Name secure_data_format -Tag "v1.0.1" -SrcPath "$base\modules\secure_data_format" -Pins $pins
$pins.secure_data_format = "v1.0.1"
Publish-Package -Name webauthnext -Tag "v1.0.7" -SrcPath "$base\modules\webauthnext" -Pins $pins
$pins.webauthnext = "v1.0.7"
Publish-Package -Name orchid_sync -Tag "v1.0.5" -SrcPath "$base\modules\orchid_sync" -Pins $pins
$pins.orchid_sync = "v1.0.5"
Publish-Package -Name identity_provider -Tag "v1.0.1" -SrcPath "$base\modules\identity_provider" -Pins $pins
$pins.identity_provider = "v1.0.1"
Publish-Package -Name secure_bootstrap -Tag "v1.0.1" -SrcPath "$base\modules\secure_bootstrap" -Pins $pins
$pins.secure_bootstrap = "v1.0.1"
Publish-Package -Name secure_boilerplate -Tag "v1.0.1" -SrcPath "$base\modules\secure_boilerplate" -Pins $pins
$pins.secure_boilerplate = "v1.0.1"

# ---- Applications ----
Publish-Package -Name social -Tag "v0.1.0" -SrcPath "$base\social" -Pins $pins
Publish-Package -Name mail -Tag "v0.1.0" -SrcPath "$base\mail" -Pins $pins
Publish-Package -Name search -Tag "v0.1.0" -SrcPath "$base\cmd\search" -Pins $pins
Publish-Package -Name services -Tag "v0.1.0" -SrcPath "$base\cmd\services" -Pins $pins
Publish-Package -Name 0trust -Tag "v0.1.0" -SrcPath "$base\cmd\0trust" -Pins $pins
Publish-Package -Name platform -Tag "v0.1.0" -SrcPath "$base\cmd\platform" -Pins $pins

Log "ALL DONE"
Log "Pin map:"
$pins.GetEnumerator() | Sort-Object Name | ForEach-Object { Log ("  {0} = {1}" -f $_.Key, $_.Value) }
