# Installing dependencies

Redlaunch runs on a Linux VPS and uses Docker Compose to build and run its own
container and the applications it manages. The installer does **not** install
host tools or Docker. Install them first, then follow the
[installation guide](INSTALL.md).

The examples below target 64-bit Ubuntu and Debian. Other distributions work
with equivalent packages, but the installer and setup scripts are tested on
Ubuntu and Debian.

## What you need

| Dependency | Needed for | Install with |
| --- | --- | --- |
| `bash` | Installer and setup scripts | `apt` |
| `git` | Cloning and updating the checkout | `apt` |
| `make` | `make setup`, `make update`, and project checks | `apt` |
| `wget` or `curl` | Downloading the installer and remote files | `apt` |
| `openssl` | Generating the session signing secret | `apt` |
| `ca-certificates` | HTTPS downloads and registry access | `apt` |
| `sudo` | Creating `/opt/redlaunch` and the `redlaunch` SSH user | `apt` |
| Docker Engine | Building and running containers | Docker's repository |
| Docker Compose plugin | Compose commands and managed applications | Docker's repository |
| `systemd` | Scheduled PostgreSQL backups and host service control | Ubuntu/Debian default |

You also need an SSH account with `sudo` access and a supported Ubuntu or
Debian release. Check the operating-system requirements in Docker's
[Ubuntu](https://docs.docker.com/engine/install/ubuntu/) and
[Debian](https://docs.docker.com/engine/install/debian/) installation guides.

## Install the host tools

Run these commands as your SSH user:

```sh
sudo apt update
sudo apt install -y ca-certificates curl wget git make bash openssl sudo
```

Ubuntu and Debian install `systemd` by default. Confirm it is available if you
plan to use scheduled backups:

```sh
systemctl --version
```

## Install Docker Engine and the Compose plugin

Use Docker's official `apt` repository. The block below works for Ubuntu;
change `distro=ubuntu` to `distro=debian` on Debian. Use the official guides
for [Ubuntu](https://docs.docker.com/engine/install/ubuntu/) and
[Debian](https://docs.docker.com/engine/install/debian/) if your release needs
different steps.

```sh
distro=ubuntu
sudo apt update
sudo apt install -y ca-certificates curl
sudo install -m 0755 -d /etc/apt/keyrings
sudo curl -fsSL "https://download.docker.com/linux/${distro}/gpg" -o /etc/apt/keyrings/docker.asc
sudo chmod a+r /etc/apt/keyrings/docker.asc
sudo tee /etc/apt/sources.list.d/docker.sources >/dev/null <<EOF
Types: deb
URIs: https://download.docker.com/linux/${distro}
Suites: $(. /etc/os-release && echo "${UBUNTU_CODENAME:-$VERSION_CODENAME}")
Components: stable
Architectures: $(dpkg --print-architecture)
Signed-By: /etc/apt/keyrings/docker.asc
EOF
sudo apt update
sudo apt install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
```

If Docker packages are already installed from the distribution, remove the
conflicting packages first as described in Docker's official installation
guide. Do not install only `docker.io` from the distribution; Redlaunch needs
the Compose plugin provided by the `docker-compose-plugin` package.

Allow your SSH user to run Docker commands without `sudo`:

```sh
sudo usermod -aG docker "$USER"
```

The `docker` group grants root-equivalent access to the host. Add only trusted
users. Log out and reconnect for the group change to apply, then verify:

```sh
docker info
docker compose version
```

Docker must be running and must accept commands from your SSH user before you
run the installer.

## Local development dependencies

Building and running Redlaunch from source needs the host tools above plus Go.
The Makefile pins Go 1.26.8 through `GOTOOLCHAIN`; with toolchain
auto-downloads enabled, a newer Go installation fetches the pinned version
automatically.

Install Go from the official downloads page:

<https://go.dev/dl/>

For a Linux `amd64` VPS, the matching archive can be installed with:

```sh
curl -fsSLO https://go.dev/dl/go1.26.8.linux-amd64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.26.8.linux-amd64.tar.gz
export PATH="$PATH:/usr/local/go/bin"
```

Replace `amd64` with `arm64` on an ARM server, and add `/usr/local/go/bin` to
your shell profile if you want the change to persist. Verify the installation:

```sh
go version
```

Running Redlaunch with `make run` requires a running user systemd manager.
Scheduled PostgreSQL backups in the bundled Docker deployment also require
host `systemd` and D-Bus, which are present on standard Ubuntu and Debian
installations.

The optional browser-logic test uses Bun. Install it from
<https://bun.sh/docs/installation> only if you want to run that test:

```sh
bun test ./internal/handler/testdata/application-environment.test.cjs
```

## Verify the installation

Run these checks on the server before starting the Redlaunch installer:

```sh
git --version
wget --version | head -n 1
make --version | head -n 1
openssl version
docker --version
docker compose version
```

Each command should print a version without a permissions error. If you are
building locally, also check:

```sh
go version
```

## Troubleshooting

### `sudo: command not found`

Install `sudo` from a root shell:

```sh
apt update
apt install -y sudo
```

### `docker: command not found`

Docker is not installed, or your shell has not reloaded its path after
installation. Reconnect over SSH and rerun `docker info`.

### `permission denied` while connecting to the Docker socket

Your user is not in the `docker` group, or the group change has not applied
yet. Run `sudo usermod -aG docker "$USER"`, log out, and reconnect.

### `docker compose` is not a Docker command

The Compose plugin is missing. Install `docker-compose-plugin` from Docker's
repository. The installer refuses to continue without `docker compose version`.

### `make: command not found`

Install the host tools:

```sh
sudo apt install -y make
```

### `go: command not found`

Add the Go binary directory to `PATH`:

```sh
export PATH="$PATH:/usr/local/go/bin"
```

Use the same line in your shell profile to make it permanent.

### TLS or certificate errors during downloads

Install or refresh the certificate bundle and check the server clock:

```sh
sudo apt install -y ca-certificates
sudo apt update
date
```

### Scheduled backups are unavailable

Scheduled backups require host `systemd` and D-Bus. Check that the unit paths
and `SYSTEMD_BINARY` setting in `docker-compose.yml` match your host. See the
[troubleshooting section](INSTALL.md#scheduled-backups-are-unavailable) in the
installation guide.

## Next steps

Once the checks pass, install Redlaunch:

- [Installation guide](INSTALL.md)
- [Main documentation](README.md)
