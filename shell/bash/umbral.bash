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
#
# Once it has fired, the trap removes itself until the next prompt (__umbral_debug_trap):
# bash marks a trap "in progress" while it runs and clears the mark only when it returns, so
# an interrupt that lands inside the trap — likely during a loop of builtins, which runs it
# before every command — left it marked, and the trap never ran again: every later command
# ran with no block. The daemon interrupts a cancelled agent command's shell, which made
# this reproducible (1 in 5 on bash 3.2, 1 in 12 on bash 5). A command that runs with no
# trap cannot be interrupted inside it, and a loop no longer pays for it on every pass.
__umbral_preexec() {
	[[ -z "${__umbral_armed-}" ]] && return 0
	[[ "${BASH_COMMAND}" == __umbral_* ]] && return 0
	unset __umbral_armed
	__umbral_in_command=1
	__umbral_esc "633;E;${BASH_COMMAND}"
	__umbral_esc "133;C"
}

# __umbral_save_status captures $? before any other prompt hook can overwrite it.
#
# The work of the prompt hook has to be split in two, because its two halves want opposite
# positions in PROMPT_COMMAND. The exit code must be read before a prompt framework's own
# hook runs, since each element of PROMPT_COMMAND leaves $? set to its own result: with
# Starship installed, a command that exited 3 was reported as 0. The prompt markers must be
# applied after that framework has rewritten PS1, or they are lost. So this runs first and
# __umbral_precmd runs last.
__umbral_save_status() {
	__umbral_status=$?
	# Returning the status leaves $? as the next hook expects to find it.
	return "${__umbral_status}"
}

# __umbral_precmd runs before each prompt. It closes the block the DEBUG trap opened, using
# the exit code __umbral_save_status captured before the other hooks ran.
__umbral_precmd() {
	local exit_code="${__umbral_status-$?}"
	if [[ -n "${__umbral_in_command-}" ]]; then
		__umbral_esc "133;D;${exit_code}"
		unset __umbral_in_command
	fi
	__umbral_cwd
	__umbral_mark_prompt
	# Arm last: everything the DEBUG trap sees from here until the next prompt is the
	# command the user typed. The trap is put back here, and the status returned through a
	# function the trap ignores: a plain `return` would be the first command it sees.
	__umbral_armed=1
	trap "${__umbral_debug_trap}" DEBUG
	__umbral_return "${exit_code}"
}

__umbral_return() { return "$1"; }

# __umbral_mark_prompt wraps PS1 in the prompt markers, every prompt rather than once.
# Starship and powerlevel10k rewrite PS1 on each prompt, so a one-time wrap is lost after
# the first command. The marker check is what stops the wrapping from stacking.
__umbral_mark_prompt() {
	[[ "${PS1}" == *$'\033]133;A'* ]] && return 0
	PS1="\[$(__umbral_esc '133;A')\]${PS1}\[$(__umbral_esc '133;B')\]"
}

# The two hooks bracket whatever the user already had: the exit code is captured before
# anything else can change it, and the prompt markers are applied after every framework has
# finished rewriting PS1.
if [[ "$(declare -p PROMPT_COMMAND 2>/dev/null)" == "declare -a"* ]]; then
	PROMPT_COMMAND=(__umbral_save_status "${PROMPT_COMMAND[@]}" __umbral_precmd)
else
	PROMPT_COMMAND="__umbral_save_status${PROMPT_COMMAND:+;${PROMPT_COMMAND}};__umbral_precmd"
fi

# Announce the integration immediately. REQ-BLK-003 gives the daemon five seconds before it
# gives up and marks the session `integration: none`, and a shell whose first prompt is slow
# would otherwise be misjudged.
__umbral_esc "133;A"
__umbral_cwd

# The trap goes last so it cannot fire for the announcement above. It is removed from the
# trap's own command, not from __umbral_preexec: bash puts back a function's DEBUG trap when
# the function returns.
__umbral_debug_trap='__umbral_preexec; [[ -z "${__umbral_in_command-}" ]] || trap - DEBUG'
trap "${__umbral_debug_trap}" DEBUG
