# Umbral shell integration for fish (REQ-BLK-005).
#
# Injected with `fish --init-command 'source <this file>'`, which runs after fish's own
# configuration rather than instead of it. Unlike bash and zsh there is nothing to restore:
# the user's config.fish is untouched.
#
# It emits, per API Spec §7 and REQ-BLK-001/002:
#   OSC 133;A            prompt start
#   OSC 133;B            prompt end, user input begins
#   OSC 633;E;<cmdline>  the command line about to run
#   OSC 133;C            command started, the daemon opens a block here
#   OSC 133;D;<exit>     command finished, the daemon closes the block
#   OSC 7;file://host/cwd working directory

status is-interactive; or exit 0

# Guard against a second injection.
if set -q __umbral_active
    exit 0
end
set -g __umbral_active 1

set -g __umbral_hostname (hostname 2>/dev/null; or echo localhost)

function __umbral_esc --description 'Write an OSC sequence terminated by BEL'
    printf '\033]%s\007' $argv[1]
end

function __umbral_cwd --description 'Report the working directory as OSC 7'
    set -l path (string replace -a ' ' '%20' -- $PWD)
    set path (string replace -a '?' '%3F' -- $path)
    set path (string replace -a '#' '%23' -- $path)
    __umbral_esc "7;file://$__umbral_hostname$path"
end

function __umbral_preexec --on-event fish_preexec
    __umbral_esc "633;E;$argv[1]"
    __umbral_esc "133;C"
    set -g __umbral_in_command 1
end

function __umbral_postexec --on-event fish_postexec
    # $status here is the exit code of the command that just finished.
    set -l exit_code $status
    if set -q __umbral_in_command
        __umbral_esc "133;D;$exit_code"
        set -e __umbral_in_command
    end
end

function __umbral_prompt --on-event fish_prompt
    __umbral_esc "133;A"
    __umbral_cwd
end

# fish has no PROMPT variable to wrap, so the prompt-end marker is emitted by wrapping
# fish_prompt itself. Copying the user's function and calling the copy leaves their prompt
# intact whatever framework produced it.
if functions -q fish_prompt; and not functions -q __umbral_original_fish_prompt
    functions -c fish_prompt __umbral_original_fish_prompt
    function fish_prompt
        __umbral_original_fish_prompt
        __umbral_esc "133;B"
    end
end

# Announce immediately: REQ-BLK-003 gives the daemon five seconds before it decides there
# is no integration.
__umbral_esc "133;A"
__umbral_cwd
