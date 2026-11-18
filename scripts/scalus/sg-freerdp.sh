#!/usr/bin/env bash
# Launch FreeRDP for a Safeguard session started from the SPP GUI via SCALUS.
#
# SCALUS parses the rdp:// URL and passes the tokens below. RemoteApp sessions
# carry a program alias (e.g. "||OISGRemoteAppLauncher (3)"); ordinary desktop
# sessions leave it empty, and FreeRDP must not then be given an empty /app:.
#
# Remmina cannot be used for RemoteApp: its RDP plugin has no RemoteApplication
# support, so the alias is silently dropped and the appliance is asked for a
# full desktop it will not give.
#
# The password is irrelevant for Safeguard gateway auth -- the session token
# inside the username authenticates -- but FreeRDP prompts if none is supplied.
set -euo pipefail

host="${1:-}"
user="${2:-}"
program="${3:-}"
name="${4:-}"

[ -n "$host" ] && [ -n "$user" ] || { echo "usage: $0 HOST USER [REMOTEAPP_PROGRAM] [REMOTEAPP_NAME]" >&2; exit 2; }

args=(/v:"$host" /u:"$user" /p:Safeguard /cert:ignore +clipboard /dynamic-resolution)

# Per-workstation extras (e.g. HiDPI scaling), one FreeRDP option per line:
#   /scale:180
#   /scale-desktop:200
conf="${SG_FREERDP_CONF:-$HOME/.SCALUS/sg-freerdp.conf}"
if [ -r "$conf" ]; then
  while IFS= read -r line; do
    line="${line%%#*}"; line="${line#"${line%%[![:space:]]*}"}"; line="${line%"${line##*[![:space:]]}"}"
    [ -n "$line" ] && args+=("$line")
  done < "$conf"
fi
if [ -n "$program" ]; then
  if [ -n "$name" ]; then
    args+=(/app:program:"$program",name:"$name")
  else
    args+=(/app:program:"$program")
  fi
fi

if [ -n "${SG_FREERDP_DRYRUN:-}" ]; then
  printf '%s\n' "/usr/bin/xfreerdp3" "${args[@]}"
  exit 0
fi
exec /usr/bin/xfreerdp3 "${args[@]}"
