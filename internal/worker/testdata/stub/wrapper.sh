#!/usr/bin/env bash
# PID 1 for the stub container. Installs a real TERM handler (so AC6/AC7's
# "a TERM trap in PID 1 fires" is observable) but must not exit on TERM:
# onstart.sh's own fail() path sends TERM to PID 1 as a courtesy before its
# own exit, and PID 1 exiting first would short-circuit that exit code.
trap 'echo WRAPPER_GOT_TERM' TERM

/onstart.sh &
child=$!
rc=
while true; do
  if wait "$child"; then rc=0; else rc=$?; fi
  # A wait() interrupted by our TERM trap returns early without
  # reaping the child, so rc can be the signal's own 128+15 instead of
  # the child's real status. Only trust rc once /proc/$child is gone,
  # which only happens once a wait() call actually reaped it.
  if [ ! -e "/proc/$child" ]; then
    break
  fi
done

if [ -f /tmp/stub.pid ]; then
  if kill -0 "$(cat /tmp/stub.pid)" 2>/dev/null; then
    echo "STUB_STILL_ALIVE"
  else
    echo "STUB_GONE"
  fi
fi

exit "${rc:-1}"
