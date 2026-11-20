#!/bin/bash
# Throwaway measurements for T-HRD-16, third pass: hard links, case, signals, Mach lookups.
set -u
BASE=$(cd "$(mktemp -d)" && pwd -P)
W=$BASE/work; D=$BASE/denied; O=$BASE/outside; mkdir -p "$W/tmp" "$D/sub" "$O"
echo secret > "$D/token"; echo secret2 > "$D/sub/inner"
export TMPDIR=$W/tmp
say() { printf '%-64s %s\n' "$1" "$2"; }
sb() { local prof=$1; shift; /usr/bin/sandbox-exec -p "$prof" "$@" >"$BASE/out" 2>"$BASE/err"; echo $?; }
err() { head -c 130 "$BASE/err" | tr '\n' ' '; }
out() { head -c 60 "$BASE/out" | tr '\n' ' '; }
BP="(version 1)(allow default)(deny file-write*)(allow file-write* (subpath \"$W\") (literal \"/dev/null\") (literal \"/dev/tty\") (literal \"/dev/zero\"))(deny file-read* (subpath \"$D\"))"
NET='(deny network-outbound (remote unix-socket))(allow network-outbound (literal "/private/var/run/mDNSResponder"))(deny lsopen)(deny appleevent-send)(deny job-creation)'
echo "== hard links made by the confined process"
say "ln denied -> W (base)" "rc=$(sb "$BP" /bin/ln "$D/token" "$W/h1") then cat rc=$(sb "$BP" /bin/cat "$W/h1") out=$(out) err=$(err)"
rm -f "$W/h1"
for rule in '(deny file-link)' "(deny file-link (subpath \"$D\"))" '(deny file-link)(allow file-link (subpath "'$W'"))' '(deny file-clone)' ; do
  rm -f "$W/h1" "$W/h2" "$W/c1"; echo own > "$W/own"
  a=$(sb "$BP$rule" /bin/ln "$D/token" "$W/h1"); e=$(err)
  b=$(sb "$BP$rule" /bin/ln "$W/own" "$W/h2")
  c=$(sb "$BP$rule" /bin/cp -c "$W/own" "$W/c1")
  say "rule $rule" "ln-denied=$a ln-own=$b clone-own=$c err=$e"
done
say "cp -c (clone) denied -> W" "rc=$(sb "$BP" /bin/cp -c "$D/token" "$W/cl") err=$(err)"
say "mv denied -> W (rename)" "rc=$(sb "$BP" /bin/mv "$D/token" "$W/mv") err=$(err)"; [ -e "$W/mv" ] && mv "$W/mv" "$D/token"
echo "== case"
UP=$(echo "$D" | sed 's#/denied$#/DENIED#')
say "fs case-insensitive?" "$([ -e "$UP/token" ] && echo yes || echo no)"
say "cat DENIED/token (no sandbox)" "$(cat "$UP/token" 2>&1 | head -c 20)"
say "cat DENIED/token" "rc=$(sb "$BP" /bin/cat "$UP/token") out=$(out) err=$(err)"
say "cat denied/TOKEN" "rc=$(sb "$BP" /bin/cat "$D/TOKEN") out=$(out)"
say "ls DENIED" "rc=$(sb "$BP" /bin/ls "$UP") out=$(out)"
WU=$(echo "$W" | sed 's#/work$#/WORK#')
say "write WORK/x (case variant of allowed)" "rc=$(sb "$BP" /bin/sh -c "echo x > $WU/x") err=$(err)"
OU=$(echo "$O" | sed 's#/outside$#/OUTSIDE#')
say "write OUTSIDE/x" "rc=$(sb "$BP" /bin/sh -c "echo x > $OU/x") err=$(err)"
echo "== signals"
for rule in '' '(deny signal (target others))' '(deny signal)' '(deny signal)(allow signal (target self))' '(deny signal)(allow signal (target same-sandbox))' '(deny signal)(allow signal (target children))' '(deny signal)(allow signal (target pgrp))' '(deny process-info* (target others))' ; do
  sleep 300 & V=$!
  rc=$(sb "$BP$rule" /bin/kill -TERM $V); e=$(err); sleep 0.3
  alive=$(kill -0 $V 2>/dev/null && echo yes || echo no)
  own=$(sb "$BP$rule" /bin/sh -c 'sleep 30 & /bin/kill -TERM $! && echo ok'); e2=$(err)
  ctrlc=$(sb "$BP$rule" /bin/sh -c 'sleep 30 & kill -INT $!; wait $!; echo done')
  say "signal ${rule:-<none>}" "kill-outside rc=$rc victim-alive=$alive kill-own-child rc=$own builtin-kill rc=$ctrlc err=$e | $e2"
  kill $V 2>/dev/null
done
echo "== mach lookups"
DNS='use Socket; $a=gethostbyname("github.com") or die "dns failed\n"; print inet_ntoa($a),"\n"'
TCP='use IO::Socket::INET; $s=IO::Socket::INET->new(PeerAddr=>"1.1.1.1",PeerPort=>443,Timeout=>5) or die "tcp: $!\n"; print "ok\n"'
for rule in '' '(deny mach-lookup)' '(deny mach-lookup)(allow mach-lookup (global-name "com.apple.system.notification_center") (global-name "com.apple.system.opendirectoryd.libinfo") (global-name "com.apple.system.logger") (global-name "com.apple.logd") (global-name "com.apple.dnssd.service") (global-name "com.apple.SystemConfiguration.DNSConfiguration") (global-name "com.apple.SystemConfiguration.configd") (global-name "com.apple.trustd") (global-name "com.apple.trustd.agent"))' '(deny mach-register)' '(deny user-preference-write)' '(deny distributed-notification-post)' '(deny mach-lookup (global-name "com.apple.cfprefsd.daemon") (global-name "com.apple.cfprefsd.agent"))' ; do
  P="$BP$NET$rule"
  a=$(sb "$P" /bin/zsh -c 'echo hi >/dev/null'); e=$(err)
  b=$(sb "$P" /bin/bash -c 'ls / >/dev/null && id -un >/dev/null')
  c=$(sb "$P" /usr/bin/perl -e "$DNS")
  d=$(sb "$P" /usr/bin/perl -e "$TCP")
  g=$(sb "$P" /bin/sh -c "cd $W && $(command -v git) init -q r$RANDOM && echo ok")
  h=$(sb "$P" /usr/bin/curl -sS -m 10 -o /dev/null https://github.com); eh=$(err)
  dw=$(sb "$P" /usr/bin/defaults write io.github.ecrespo.umbral.probe k v$RANDOM); ed=$(err)
  dr=$(/usr/bin/defaults read io.github.ecrespo.umbral.probe k 2>/dev/null | head -c 12)
  /usr/bin/defaults delete io.github.ecrespo.umbral.probe >/dev/null 2>&1
  go_=$(sb "$P" /bin/sh -c "command -v go >/dev/null && cd $W && GOCACHE=$W/gocache GOPATH=$W/gopath GOFLAGS=-mod=mod go version >/dev/null")
  echo "[mach] ${rule:-<none>}" | cut -c1-150
  echo "       zsh=$a bash+id=$b dns=$c tcp=$d git-init=$g curl-https=$h go=$go_ defaults-write=$dw persisted='$dr'  err=$e | curl: $eh | defaults: $ed" | cut -c1-420
done
echo "== ways out again under the full profile (with mach-lookup denied + allowlist)"
echo done
