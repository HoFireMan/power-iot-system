#!/bin/sh
set -eu

# App-VM-side narrow DB control seam. Production uses a host-managed SSH
# destination; rehearsal may select the packaged local helper explicitly.
transport=${D6_DB_CONTROL_TRANSPORT:-ssh}
case "$transport" in
  local)
    command=${D6_DB_LOCAL_CONTROL_COMMAND:-/opt/poweriot/d6-db-local-control.sh}
    [ -x "$command" ] || { echo 'DB local control helper is missing' >&2; exit 2; }
    exec "$command" "$@"
    ;;
  ssh)
    host=${D6_DB_SSH_HOST:?set DB VM SSH host}
    user=${D6_DB_SSH_USER:?set DB VM SSH user}
    remote=${D6_DB_REMOTE_CONTROL_PATH:-/opt/poweriot/d6-db-local-control.sh}
    [ -n "$host" ] || { echo 'DB VM SSH host is empty' >&2; exit 2; }
    exec ssh -o BatchMode=yes -o ConnectTimeout=5 -o StrictHostKeyChecking=yes \
      "$user@$host" "$remote" "$@"
    ;;
  *) echo 'D6_DB_CONTROL_TRANSPORT must be local or ssh' >&2; exit 2 ;;
esac
