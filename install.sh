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

readonly repository_url='https://github.com/redbolttechnologies/redlaunch.git'
readonly default_install_dir='/opt/redlaunch'

privileged() {
	if [[ $EUID -eq 0 ]]; then
		"$@"
	elif command -v sudo >/dev/null 2>&1; then
		sudo "$@"
	else
		fail 'need root privileges for this installation path; install sudo or run as root'
	fi
}

trim_whitespace() {
	local value=$1

	value="${value#"${value%%[![:space:]]*}"}"
	value="${value%"${value##*[![:space:]]}"}"
	printf '%s' "$value"
}

resolve_install_dir() {
	local configured
	configured=$(trim_whitespace "${REDLAUNCH_INSTALL_DIR:-}")
	if [[ -n $configured ]]; then
		printf '%s' "$configured"
		return
	fi

	local prompt="Installation directory [$default_install_dir]: "
	local input=''

	# When streamed into Bash, standard input contains the installer itself.
	# Keep the installation-path prompt connected to the user's terminal.
	# Probe /dev/tty in a subshell: a mere readability test still succeeds on
	# systems without a controlling terminal, while the open itself fails.
	if [[ -t 0 ]]; then
		printf '%s' "$prompt" >&2
		IFS= read -r input || fail 'input interrupted'
	elif ( : <>/dev/tty ) 2>/dev/null; then
		exec 3<>/dev/tty
		printf '%s' "$prompt" >&3
		IFS= read -r input <&3 || fail 'input interrupted'
		exec 3<&-
	else
		printf '%s' "$default_install_dir"
		return
	fi

	input=$(trim_whitespace "$input")
	if [[ -z $input ]]; then
		printf '%s' "$default_install_dir"
	else
		printf '%s' "$input"
	fi
}

install_dir=$(resolve_install_dir)
readonly install_dir
[[ -n $install_dir ]] || fail 'installation directory must not be empty'
[[ $install_dir == /* ]] || fail "installation directory must be an absolute path: $install_dir"
[[ $install_dir != '/' ]] || fail 'installation directory must not be /'

require_command git
require_command make
require_command docker
docker compose version >/dev/null 2>&1 ||
	fail 'Docker Compose is not available; install Docker Engine with the Compose plugin'

if [[ -e $install_dir || -L $install_dir ]]; then
	fail "installation directory already exists: $install_dir"
fi

parent_dir=$(dirname -- "$install_dir")
if ! mkdir -p -- "$parent_dir" 2>/dev/null; then
	privileged mkdir -p -- "$parent_dir" ||
		fail "could not create parent directory: $parent_dir"
fi

if [[ ! -w $parent_dir ]]; then
	# System locations such as /opt need root to create the target directory.
	# Pre-create it owned by the invoking user so `git clone` runs unprivileged.
	if [[ $EUID -eq 0 && -n ${SUDO_USER:-} && $SUDO_USER != 'root' ]]; then
		invoking_user=$SUDO_USER
	else
		invoking_user=$(id -un) || fail 'could not determine the current user'
	fi
	privileged mkdir -p -- "$install_dir" ||
		fail "could not create installation directory: $install_dir"
	privileged chown "$invoking_user" "$install_dir" ||
		fail "could not set ownership on: $install_dir"
fi

printf 'Cloning Redlaunch into %s...\n' "$install_dir"
git clone -- "$repository_url" "$install_dir" ||
	fail "could not clone into: $install_dir"

# When the installer itself runs via sudo, hand the checkout back to the
# original user so later setup and update steps work without root.
if [[ $EUID -eq 0 && -n ${SUDO_USER:-} && $SUDO_USER != 'root' ]]; then
	chown -R "$SUDO_USER" "$install_dir" ||
		fail "could not set ownership on: $install_dir"
fi

cd -- "$install_dir"
printf 'Running make setup...\n'

# When streamed into Bash, standard input contains the installer itself. Keep
# the interactive setup prompts connected to the user's terminal when one is
# available; otherwise run non-interactively.
if [[ ! -t 0 ]] && ( : </dev/tty ) 2>/dev/null; then
	exec </dev/tty
	exec make setup
fi

exec make setup
