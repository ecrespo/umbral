#!/bin/bash
# Throwaway measurements for T-HRD-16, second pass: sockets, baselines for the ways out.
set -u
BASE=$(cd "$(mktemp -d)" && pwd -P)
W=$BASE/work; D=$BASE/denied; O=$BASE/outside; mkdir -p "$W/tmp" "$D" "$O"
export TMPDIR=$W/tmp
say() { printf '%-60s %s\n' "$1" "$2"; }
sb() { local prof=$1; shift; /usr/bin/sandbox-exec -p "$prof" "$@" >"$BASE/out" 2>"$BASE/err"; echo $?; }
err() { head -c 150 "$BASE/err" | tr '\n' ' '; }
BASEPROF="(version 1)(allow default)(deny file-write*)(allow file-write* (subpath \"$W\") (literal \"/dev/null\") (literal \"/dev/tty\") (literal \"/dev/zero\"))(deny file-read* (subpath \"$D\"))"
echo "== unix sockets (perl)"
SOCK=$O/s.sock
perl -MIO::Socket::UNIX -e '$s=IO::Socket::UNIX->new(Local=>$ARGV[0],Listen=>5) or die "listen: $!"; open(F,">",$ARGV[1]); close F; $SIG{ALRM}=sub{exit 0}; alarm 120; while($c=$s->accept){print $c "hi"; close $c}' "$SOCK" "$BASE/ready" &
SRV=$!
for i in $(seq 50); do [ -e "$BASE/ready" ] && break; sleep 0.1; done
CONNECT='use IO::Socket::UNIX; $s=IO::Socket::UNIX->new(Peer=>$ARGV[0]) or die "connect: $!\n"; print "ok\n"'
PAIR='use Socket; socketpair(A,B,AF_UNIX,SOCK_STREAM,0) or die "pair: $!\n"; print "ok\n"'
BINDW='use IO::Socket::UNIX; $s=IO::Socket::UNIX->new(Local=>$ARGV[0],Listen=>1) or die "bind: $!\n"; print "ok\n"'
TCP='use IO::Socket::INET; $s=IO::Socket::INET->new(PeerAddr=>"1.1.1.1",PeerPort=>443,Timeout=>5) or die "tcp: $!\n"; print "ok\n"'
DNS='use Socket; $a=gethostbyname("github.com") or die "dns failed\n"; print inet_ntoa($a),"\n"'
LOOP='use IO::Socket::INET; $l=IO::Socket::INET->new(LocalAddr=>"127.0.0.1",LocalPort=>0,Listen=>1) or die "listen: $!\n"; $c=IO::Socket::INET->new(PeerAddr=>"127.0.0.1",PeerPort=>$l->sockport) or die "loop: $!\n"; print "ok\n"'
say "no sandbox: connect" "$(perl -e "$CONNECT" "$SOCK" >/dev/null 2>&1; echo $?)"
i=0
for rule in \
  '' \
  '(deny network-outbound (remote unix-socket))' \
  '(deny network-outbound (remote unix-socket (path-regex #"^/")))' \
  '(deny network* (local unix-socket))' \
  '(deny network-outbound (regex #"^/"))' \
  '(deny network-outbound (subpath "/"))' \
  '(deny network-outbound (literal "'$SOCK'"))' \
  '(deny system-socket (socket-domain AF_UNIX))' \
  '(deny system-socket (socket-domain 1))' \
  '(deny system-socket)' \
  '(deny network-outbound (remote unix-socket))(deny network-bind (local unix-socket))' \
  '(deny network-outbound (remote unix))' \
  '(deny network-outbound (remote unix-socket))(allow network-outbound (literal "/private/var/run/mDNSResponder"))' \
  '(deny network-outbound (remote unix-socket))(allow network-outbound (remote unix-socket (path-literal "/private/var/run/mDNSResponder")))' \
  '(deny network*)' \
  '(deny network*)(allow network* (remote ip "localhost:*"))(allow network* (local ip "localhost:*"))' ; do
  i=$((i+1)); rm -f "$W/own.sock"; P="$BASEPROF$rule"
  c=$(sb "$P" /usr/bin/perl -e "$CONNECT" "$SOCK"); e=$(err)
  p=$(sb "$P" /usr/bin/perl -e "$PAIR")
  b=$(sb "$P" /usr/bin/perl -e "$BINDW" "$W/own.sock")
  t=$(sb "$P" /usr/bin/perl -e "$TCP")
  d=$(sb "$P" /usr/bin/perl -e "$DNS")
  l=$(sb "$P" /usr/bin/perl -e "$LOOP")
  echo "[$i] ${rule:-<none>}"
  echo "     connect=$c socketpair=$p bind-in-W=$b tcp=$t dns=$d loopback=$l  err=$e"
done
kill $SRV 2>/dev/null
echo "== ways out: baselines without a sandbox, then with"
PL=$W/job.plist
cat > "$PL" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict><key>Label</key><string>probe.escape</string>
<key>ProgramArguments</key><array><string>/usr/bin/touch</string><string>$O/escaped</string></array>
<key>RunAtLoad</key><true/></dict></plist>
PLIST
cat > "$W/run.command" <<CMD
#!/bin/sh
touch $O/escaped
CMD
chmod +x "$W/run.command"
U=$(id -u)
launchctl print gui/$U >/dev/null 2>&1; say "gui domain present" "rc=$?"
launchctl print user/$U >/dev/null 2>&1; say "user domain present" "rc=$?"
try() { # NAME PROFILE(or "none") CMD...
  local name=$1 prof=$2; shift 2; rm -f "$O/escaped"; local rc
  if [ "$prof" = none ]; then "$@" >"$BASE/out" 2>"$BASE/err"; rc=$?; else rc=$(sb "$prof" "$@"); fi
  local n=0; while [ $n -lt 16 ] && [ ! -e "$O/escaped" ]; do sleep 0.5; n=$((n+1)); done
  say "$name" "rc=$rc escaped=$([ -e "$O/escaped" ] && echo YES || echo no) err=$(err)"
}
clean() { launchctl remove probe.submit 2>/dev/null; launchctl bootout gui/$U/probe.escape 2>/dev/null; launchctl bootout user/$U/probe.escape 2>/dev/null; launchctl unload "$PL" 2>/dev/null; pkill -x Terminal 2>/dev/null; }
for prof in none "$BASEPROF" "$BASEPROF(deny job-creation)" "$BASEPROF(deny lsopen)(deny appleevent-send)(deny job-creation)" "$BASEPROF(deny mach-lookup)" "$BASEPROF(deny mach-lookup (global-name-regex #\"^com\\.apple\\.(coreservices|lsd|launchservicesd)\"))"; do
  echo "-- profile: ${prof#"$BASEPROF"}"
  try "launchctl submit" "$prof" /bin/launchctl submit -l probe.submit -- /usr/bin/touch "$O/escaped"; clean
  try "launchctl bootstrap gui" "$prof" /bin/launchctl bootstrap gui/$U "$PL"; clean
  try "launchctl bootstrap user" "$prof" /bin/launchctl bootstrap user/$U "$PL"; clean
  try "launchctl load" "$prof" /bin/launchctl load "$PL"; clean
  try "launchctl asuser" "$prof" /bin/launchctl asuser $U /usr/bin/touch "$O/escaped"; clean
  try "open run.command" "$prof" /usr/bin/open "$W/run.command"; clean
  try "open -a Terminal run.command" "$prof" /usr/bin/open -a Terminal "$W/run.command"; clean
  try "osascript Terminal do script" "$prof" /usr/bin/osascript -e "tell application \"Terminal\" to do script \"touch $O/escaped\""; clean
  try "osascript System Events" "$prof" /usr/bin/osascript -e "tell application \"System Events\" to do shell script \"touch $O/escaped\""; clean
  try "at now" "$prof" /bin/sh -c "echo 'touch $O/escaped' | at now"; clean
  try "crontab install" "$prof" /bin/sh -c "echo '* * * * * touch $O/escaped' | crontab -"; crontab -r 2>/dev/null
  try "dns still works" "$prof" /usr/bin/perl -e "$DNS"
done
echo "== signals"
for rule in '' '(deny signal (target others))' '(deny signal)' '(deny signal)(allow signal (target self))' '(deny signal)(allow signal (target same-sandbox))' '(deny signal)(allow signal (target children))' '(deny signal)(allow signal (target pgrp))'; do
  sleep 300 & V=$!
  rc=$(sb "$BASEPROF$rule" /bin/kill -TERM $V); sleep 0.3
  own=$(sb "$BASEPROF$rule" /bin/sh -c 'sleep 30 & /bin/kill -TERM $! && echo ok')
  say "signal rule ${rule:-<none>}" "kill-outside rc=$rc victim-alive=$(kill -0 $V 2>/dev/null && echo yes || echo no) kill-own-child rc=$own err=$(err)"
  kill $V 2>/dev/null
done
echo "== git, system and homebrew"
R=$BASE/repo; git init -q "$R"; git -C "$R" -c user.name=a -c user.email=a@b commit -q --allow-empty -m init
git -C "$R" worktree add -q "$W/wt" -b t1 2>/dev/null
GP="(version 1)(allow default)(deny file-write*)(allow file-write* (subpath \"$W\") (literal \"/dev/null\") (subpath \"$R/.git/worktrees/wt\") (subpath \"$R/.git/objects\") (subpath \"$R/.git/refs\") (subpath \"$R/.git/logs\"))"
for g in "$(command -v git)" /usr/bin/git; do
  echo f$RANDOM > "$W/wt/f"
  rc=$(sb "$GP" /bin/sh -c "cd $W/wt && $g add -A && $g -c user.name=a -c user.email=a@b commit -q -m c && $g log --oneline | head -1")
  say "$g ($($g --version)) commit" "rc=$rc out=$(head -1 $BASE/out) err=$(err)"
done
say "branch landed in main repo" "$(git -C "$R" log --oneline t1 | wc -l | tr -d ' ') commits on t1"
echo "== misc"
say "write /dev/tty" "$(sb "$BASEPROF" /bin/sh -c 'echo x > /dev/tty'); err=$(err)"
say "write /dev/stderr" "$(sb "$BASEPROF" /bin/sh -c 'echo x > /dev/stderr'); err=$(err)"
say "open pty (script)" "$(sb "$BASEPROF" /usr/bin/script -q /dev/null /usr/bin/true); err=$(err)"
say "read ~/.ssh listing" "$(sb "$BASEPROF" /bin/ls $HOME/.ssh); "
say "exec from W" "$(cp /usr/bin/true $W/t 2>/dev/null; sb "$BASEPROF" $W/t)"
say "go present" "$(command -v go) $(go version 2>/dev/null)"
echo done
