# Umbral shell integration for zsh (REQ-BLK-005).
#
# Injected by pointing ZDOTDIR at a directory holding this file. zsh then reads this
# instead of the user's own .zshrc, so the first thing it does is restore ZDOTDIR and load
# the real configuration. Everything after that is added on top, which is what keeps a
# custom prompt working.
#
# It emits, per API Spec §7 and REQ-BLK-001/002:
#   OSC 133;A            prompt start
#   OSC 133;B            prompt end, user input begins
#   OSC 633;E;<cmdline>  the command line about to run
#   OSC 133;C            command started, the daemon opens a block here
#   OSC 133;D;<exit>     command finished, the daemon closes the block
#   OSC 7;file://host/cwd working directory

# Restore the user's ZDOTDIR before sourcing anything, so their configuration sees the
# value it expects rather than Umbral's temporary directory.
if [[ -n "${UMBRAL_ZDOTDIR-}" ]]; then
	ZDOTDIR="${UMBRAL_ZDOTDIR}"
else
	unset ZDOTDIR
fi
unset UMBRAL_ZDOTDIR

# zsh already read .zshenv from the original ZDOTDIR before this file, so only .zshrc has
# to be loaded here.
if [[ -f "${ZDOTDIR:-$HOME}/.zshrc" ]]; then
	source "${ZDOTDIR:-$HOME}/.zshrc"
fi

[[ -o interactive ]] || return 0

# Guard against a second injection.
if [[ -n "${__umbral_active-}" ]]; then
	return 0
fi
typeset -g __umbral_active=1

typeset -g __umbral_hostname="${HOST:-${HOSTNAME:-localhost}}"

# __umbral_esc writes an OSC sequence terminated by BEL.
__umbral_esc() { printf '\033]%s\007' "$1" }

# __umbral_cwd reports the working directory as a file URL (OSC 7). zsh's :gs modifiers do
# the small amount of escaping that actually matters for a URL.
__umbral_cwd() {
	local path="${PWD}"
	path="${path:gs/ /%20}"
	path="${path:gs/?/%3F}"
	path="${path:gs/#/%23}"
	__umbral_esc "7;file://${__umbral_hostname}${path}"
}

# preexec receives the command line zsh is about to run, so unlike bash there is no trap to
# guard and no history to consult.
__umbral_preexec() {
	__umbral_esc "633;E;$1"
	__umbral_esc "133;C"
	__umbral_in_command=1
}

# precmd runs before each prompt and must read $? first, before any other hook sees it.
__umbral_precmd() {
	local exit_code=$?
	if [[ -n "${__umbral_in_command-}" ]]; then
		__umbral_esc "133;D;${exit_code}"
		unset __umbral_in_command
	fi
	__umbral_cwd
	__umbral_mark_prompt
}

# __umbral_mark_prompt wraps PROMPT every prompt rather than once: powerlevel10k and
# starship rewrite it on each prompt, so a one-time wrap is lost after the first command.
# %{...%} marks the sequence as zero-width so it does not corrupt line wrapping.
__umbral_mark_prompt() {
	[[ "${PROMPT}" == *$'\033]133;A'* ]] && return 0
	PROMPT="%{$(__umbral_esc '133;A')%}${PROMPT}%{$(__umbral_esc '133;B')%}"
}

autoload -Uz add-zsh-hook
add-zsh-hook precmd __umbral_precmd
add-zsh-hook preexec __umbral_preexec

# Announce immediately, before the first prompt, so REQ-BLK-003's five-second window is not
# spent waiting on a slow prompt framework.
__umbral_esc "133;A"
__umbral_cwd
