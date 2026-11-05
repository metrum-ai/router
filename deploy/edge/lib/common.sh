# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
#
# Shared defaults for the edge bundle scripts. Sourced by install.sh, demo.sh,
# upgrade.sh and uninstall.sh so the four agree on where a bundle lives.
#
# A root install cannot use "$HOME/metrum-edge": under sudo that is
# /root/metrum-edge, and /root is mode 0700, so the router container (uid 65532)
# cannot traverse it. Root therefore gets a system path, and every script applies the
# same rule, so `sudo bash demo.sh` finds what `sudo bash install.sh` created.

metrum_default_data_dir() {
  if [ "$(id -u)" = "0" ]; then
    printf '%s\n' "/var/lib/metrum-edge"
  else
    printf '%s/metrum-edge\n' "${HOME:-/tmp}"
  fi
}

# metrum_config_path_for_runtime <host-config-path> <mode> prints the path the router
# process sees: docker mode mounts the data directory at /app, so a host path is wrong
# inside the container.
metrum_config_path_for_runtime() {
  local host_path="$1" mode="$2"
  if [ "$mode" = "docker" ]; then
    printf '/app/config/%s\n' "$(basename "$host_path")"
  else
    printf '%s\n' "$host_path"
  fi
}
