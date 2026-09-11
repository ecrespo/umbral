# Umbral shell integration for bash (REQ-BLK-005).
#
# Injected with `bash --init-file <this file>`, which replaces ~/.bashrc rather than
# adding to it, so the first thing this script does is load the user's own configuration.
# Everything after that is added on top, which is what keeps a custom prompt working.
#
# It emits, per API Spec §7 and REQ-BLK-001/002:
#   OSC 133;A            prompt start
#   OSC 133;B            prompt end, user input begins
#   OSC 633;E;<cmdline>  the command line about to run
#   OSC 133;C            command started, the daemon opens a block here
#   OSC 133;D;<exit>     command finished, the daemon closes the block
#   OSC 7;file://host/cwd working directory
#
# Non-interactive shells get nothing: hooks in a script would corrupt its output.

case $- in
*i*) ;;
*) return 0 ;;
esac

# Load the user's own configuration first. --init-file suppressed it, and a terminal that
# silently drops someone's bashrc is worse than one with no integration at all.
if [[ -n "${UMBRAL_BASH_RC-}" && -f "${UMBRAL_BASH_RC}" ]]; then
	. "${UMBRAL_BASH_RC}"
elif [[ -f "${HOME}/.bashrc" ]]; then
	. "${HOME}/.bashrc"
fi

# Guard against a second injection, for example a nested `bash --init-file`.
if [[ -n "${__umbral_active-}" ]]; then
	return 0
fi
__umbral_active=1

__umbral_hostname="${HOSTNAME:-$(uname -n 2>/dev/null || echo localhost)}"

# __umbral_esc writes an OSC sequence terminated by BEL, which every terminal accepts and
# which cannot be confused with the ST form inside a command line.
__umbral_esc() { printf '\033]%s\007' "$1"; }

# __umbral_cwd reports the working directory as a file URL (OSC 7). Percent-encoding is
# limited to the characters that actually break the URL; a full encoder in bash would be
# slower than the prompt it runs in.
__umbral_cwd() {
	local path="${PWD}"
	path="${path// /%20}"
	path="${path//\?/%3F}"
	path="${path//#/%23}"
	__umbral_esc "7;file://${__umbral_hostname}${path}"
}

# __umbral_preexec runs from the DEBUG trap, which fires far more often than once per
# command: for every element of PROMPT_COMMAND, for every command in a pipeline, and for
# this script's own last lines.
#
# `__umbral_armed` is what narrows it to the one occurrence that matters. It is set at the
# end of the prompt hook, so the next command the trap sees is the one the user typed;
# firing it disarms until the following prompt. Without this, a prompt framework's own
# hook opens a phantom block: with Starship installed, the first block of every session
# recorded `starship_precmd` as its command.
#
# The arrangement assumes this hook is the last element of PROMPT_COMMAND, which is how it
# is installed below. Something appended afterwards would run inside the armed window and
# be mistaken for a user command.
__umbral_preexec() {
	[[ -z "${__umbral_armed-}" ]] && return 0
	[[ "${BASH_COMMAND}" == __umbral_* ]] && return 0
	unset __umbral_armed
	__umbral_in_command=1
	__umbral_esc "633;E;${BASH_COMMAND}"
	__umbral_esc "133;C"
}

# __umbral_precmd runs before each prompt. It closes the block the DEBUG trap opened, and
# must be the first thing to read $?, before any other hook overwrites it.
__umbral_precmd() {
	local exit_code=$?
	if [[ -n "${__umbral_in_command-}" ]]; then
		__umbral_esc "133;D;${exit_code}"
		unset __umbral_in_command
	fi
	__umbral_cwd
	__umbral_mark_prompt
	# Arm last: everything the DEBUG trap sees from here until the next prompt is the
	# command the user typed.
	__umbral_armed=1
	return "${exit_code}"
}

# __umbral_mark_prompt wraps PS1 in the prompt markers, every prompt rather than once.
# Starship and powerlevel10k rewrite PS1 on each prompt, so a one-time wrap is lost after
# the first command. The marker check is what stops the wrapping from stacking.
__umbral_mark_prompt() {
	[[ "${PS1}" == *$'\033]133;A'* ]] && return 0
	PS1="\[$(__umbral_esc '133;A')\]${PS1}\[$(__umbral_esc '133;B')\]"
}

# The hook goes last in PROMPT_COMMAND so a prompt framework has already rewritten PS1 by
# the time the markers are applied.
if [[ "$(declare -p PROMPT_COMMAND 2>/dev/null)" == "declare -a"* ]]; then
	PROMPT_COMMAND+=(__umbral_precmd)
else
	PROMPT_COMMAND="${PROMPT_COMMAND:+${PROMPT_COMMAND};}__umbral_precmd"
fi

# Announce the integration immediately. REQ-BLK-003 gives the daemon five seconds before it
# gives up and marks the session `integration: none`, and a shell whose first prompt is slow
# would otherwise be misjudged.
__umbral_esc "133;A"
__umbral_cwd

# The trap goes last so it cannot fire for the announcement above.
trap '__umbral_preexec' DEBUG
