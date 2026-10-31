# Umbral shell integration for PowerShell 7 (REQ-BLK-008, REQ-WIN-002).
#
# Dot-sourced by `pwsh -NoExit -Command ". '<this file>'"`, after the user's profiles have
# loaded, so their prompt is the one wrapped. It emits, per API Spec §7 and REQ-BLK-001/002:
#   OSC 133;A            prompt start
#   OSC 133;B            prompt end, user input begins
#   OSC 633;E;<cmdline>  the command line about to run
#   OSC 133;C            command started, the daemon opens a block here
#   OSC 133;D;<exit>     command finished, the daemon closes the block
#   OSC 7;file://host/cwd working directory
#
# A host other than the console (a script, the ISE) gets nothing.

if ($Host.Name -ne 'ConsoleHost') { return }
# Guard against a second injection, for example a nested pwsh dot-sourcing this again.
if ($global:__UmbralActive) { return }
$global:__UmbralActive = $true
$global:__UmbralInCommand = $false
$global:__UmbralErrorsBefore = 0
$global:__UmbralHost = [System.Net.Dns]::GetHostName()
$global:__UmbralPrompt = $function:prompt
$global:__UmbralReadLine = $function:PSConsoleHostReadLine

# __UmbralEsc is one OSC sequence terminated by BEL, which cannot be confused with the ST form
# inside a command line.
function global:__UmbralEsc([string]$Payload) { "$([char]0x1b)]$Payload$([char]7)" }

# __UmbralCwd is the working directory as a file URL (OSC 7), for the file system provider
# only: a prompt in HKLM:\ has no directory to report.
function global:__UmbralCwd {
	$loc = $ExecutionContext.SessionState.Path.CurrentLocation
	if ($loc.Provider.Name -ne 'FileSystem') { return '' }
	$path = $loc.ProviderPath -replace '\\', '/'
	if (-not $path.StartsWith('/')) { $path = '/' + $path }
	$path = $path -replace '%', '%25' -replace ' ', '%20' -replace '#', '%23' -replace '\?', '%3F'
	__UmbralEsc "7;file://$($global:__UmbralHost)$path"
}

# prompt closes the block the read-line hook opened, then wraps the user's own prompt in the
# prompt markers. $? is read first, before anything here can change it. A failed native command
# reports its own exit code; a failed cmdlet, which has none, reports 1.
function global:prompt {
	$ok = $?
	$code = $global:LASTEXITCODE
	$out = ''
	if ($global:__UmbralInCommand) {
		$exit = 0
		if (-not $ok) {
			$exit = 1
			if ($global:Error.Count -le $global:__UmbralErrorsBefore -and $code -is [int] -and $code -ne 0) {
				$exit = $code
			}
		}
		$out += __UmbralEsc "133;D;$exit"
		$global:__UmbralInCommand = $false
	}
	$out += __UmbralCwd
	$out += __UmbralEsc '133;A'
	$inner = if ($global:__UmbralPrompt) { & $global:__UmbralPrompt } else { "PS $($ExecutionContext.SessionState.Path.CurrentLocation)> " }
	# The user's prompt may have run commands of its own; the next one reads what they left.
	$global:LASTEXITCODE = $code
	"$out$inner$(__UmbralEsc '133;B')"
}

# PSConsoleHostReadLine is PSReadLine's entry point: the line it returns is the one about to
# run, so the command line and the block's start are announced here, after Enter and before
# anything executes. An empty line opens no block, as in the other shells.
if ($global:__UmbralReadLine) {
	function global:PSConsoleHostReadLine {
		$line = & $global:__UmbralReadLine
		if ($line -and $line.Trim()) {
			$global:__UmbralErrorsBefore = $global:Error.Count
			$global:__UmbralInCommand = $true
			$flat = $line -replace '[\x00-\x1f\x7f]', ' '
			[Console]::Write((__UmbralEsc "633;E;$flat") + (__UmbralEsc '133;C'))
		}
		$line
	}
}

# Announce the integration immediately. REQ-BLK-003 gives the daemon five seconds before it
# marks the session `integration: none`, and a slow first prompt would otherwise be misjudged.
[Console]::Write((__UmbralEsc '133;A') + (__UmbralCwd))
