#!/usr/bin/env sh
# Figures out how to reach the embedding server from inside a container,
# by testing hops explicitly instead of guessing from docs. Handles the
# WSL2 double-hop case: container -> WSL2 distro -> Windows host, where
# host.containers.internal only gets you to WSL2, not to Windows.
#
#   ./scripts/detect-embed-url.sh [port]     # port defaults to 1234
set -eu
port="${1:-1234}"

engine=""
if command -v podman >/dev/null 2>&1; then
	engine=podman
elif command -v docker >/dev/null 2>&1; then
	engine=docker
else
	echo "no podman or docker on PATH" >&2
	exit 1
fi
echo "engine: $engine"

probe() { # probe <label> <ip>
	label="$1"; ip="$2"
	printf '%-32s %-16s ' "$label" "$ip"
	if "$engine" run --rm --add-host=host.containers.internal:host-gateway alpine \
		sh -c "command -v nc >/dev/null 2>&1 || apk add --no-cache -q busybox-extras >/dev/null 2>&1; nc -z -w3 $ip $port" 2>/dev/null; then
		echo "OK"
		echo "$ip"
		return 0
	fi
	echo "unreachable"
	return 1
}

echo "checking port $port from inside a container, trying each hop:"
echo

# Hop 1: the container's immediate host (WSL2 distro itself, or the real
# host on native Linux / podman machine). This is what
# host.containers.internal resolves to.
hop1=$("$engine" run --rm --add-host=host.containers.internal:host-gateway \
	alpine getent hosts host.containers.internal 2>/dev/null | awk '{print $1}' || true)
found=""
if [ -n "$hop1" ] && probe "host.containers.internal" "$hop1" >/tmp/hop1.out; then
	found=$(tail -1 /tmp/hop1.out)
fi
cat /tmp/hop1.out 2>/dev/null || true

# Hop 2 (WSL2 only): if hop 1 failed and we're inside WSL, the embedding
# server may be one more hop up, on the actual Windows host. WSL exposes
# that address as the default route gateway.
if [ -z "$found" ] && [ -f /proc/version ] && grep -qi microsoft /proc/version 2>/dev/null; then
	hop2=$(ip route show default 2>/dev/null | awk '{print $3}' || true)
	if [ -n "$hop2" ] && [ "$hop2" != "$hop1" ]; then
		if probe "WSL2->Windows gateway" "$hop2" >/tmp/hop2.out; then
			found=$(tail -1 /tmp/hop2.out)
		fi
		cat /tmp/hop2.out
	fi
fi

echo
if [ -n "$found" ]; then
	echo "Use in compose/.env:"
	echo "  ENGRAM_EMBED_URL=http://${found}:${port}"
	if [ "$found" != "$hop1" ]; then
		echo
		echo "NOTE: this is the WSL2->Windows gateway IP, which changes on"
		echo "every 'wsl --shutdown'. Re-run this script after any WSL"
		echo "restart and update compose/.env accordingly — or switch"
		echo "WSL to 'networkingMode=mirrored' in .wslconfig, where plain"
		echo "'localhost' works from either side and this stops mattering."
	fi
else
	echo "Nothing reachable on port $port at either hop."
	echo "  -> is the embedding server (e.g. LM Studio) actually running?"
	echo "  -> is it set to serve on the network (0.0.0.0), not just"
	echo "     127.0.0.1 / localhost-only, on its own machine?"
	echo "  -> if on Windows behind a firewall, check that inbound rules"
	echo "     allow the WSL vEthernet adapter."
	exit 1
fi