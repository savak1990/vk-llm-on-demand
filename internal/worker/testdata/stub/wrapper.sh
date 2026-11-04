#!/usr/bin/env bash
# PID 1 for the stub container. Installs a real TERM handler (so AC6/AC7's
# "a TERM trap in PID 1 fires" is observable) but must not exit on TERM:
# onstart.sh's own fail() path sends TERM to PID 1 as a courtesy before its
# own exit, and PID 1 exiting first would short-circuit that exit code.
trap 'echo WRAPPER_GOT_TERM' TERM

rm -f /tmp/onstart.rc
( /onstart.sh; echo $? > /tmp/onstart.rc ) &
child=$!

# Don't trust wait()'s own return value: a TERM arriving while this call
# is blocked can make bash report the signal's pseudo-status (128+15)
# instead of the child's real exit code, even once the child has truly
# been reaped. The subshell above writes the real code to a file right
# after onstart.sh itself exits, independent of any signal racing this
# wait call, so that file is the only value this script trusts.
wait "$child" 2>/dev/null || true
while [ ! -f /tmp/onstart.rc ]; do
  sleep 0.1
done
rc=$(cat /tmp/onstart.rc)

if [ -f /tmp/stub.pid ]; then
  if kill -0 "$(cat /tmp/stub.pid)" 2>/dev/null; then
    echo "STUB_STILL_ALIVE"
  else
    echo "STUB_GONE"
  fi
fi

exit "$rc"
