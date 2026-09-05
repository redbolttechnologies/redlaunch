#!/usr/bin/env bash

set -Eeuo pipefail

fail() {
	printf 'install: %s\n' "$*" >&2
	exit 1
}

require_command() {
	local command_name=$1

	command -v "$command_name" >/dev/null 2>&1 ||
		fail "required command not found: $command_name"
}

[[ -n ${HOME:-} ]] || fail '$HOME is not set'

readonly repository_url='https://github.com/redbolttechnologies/redlaunch.git'
readonly install_dir="${REDLAUNCH_INSTALL_DIR:-$HOME/redlaunch}"

require_command git
require_command make
require_command docker
docker compose version >/dev/null 2>&1 ||
	fail 'Docker Compose is not available; install Docker Engine with the Compose plugin'

if [[ -e $install_dir || -L $install_dir ]]; then
	fail "installation directory already exists: $install_dir"
fi

mkdir -p -- "$(dirname -- "$install_dir")"
printf 'Cloning Redlaunch into %s...\n' "$install_dir"
git clone -- "$repository_url" "$install_dir"

cd -- "$install_dir"
printf 'Running make setup...\n'

# When streamed into Bash, standard input contains the installer itself. Keep
# the interactive setup prompts connected to the user's terminal.
if [[ ! -t 0 && -r /dev/tty ]]; then
	exec make setup </dev/tty
fi

exec make setup
