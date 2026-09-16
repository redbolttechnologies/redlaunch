# Server SSH keys

The **Settings → SSH keys** tab creates Ed25519 key pairs for external
automation, for example GitHub Actions, to SSH into this server itself.

Each key has a display name used only in the Redlaunch list, for example
`GHA migrator workflow access`. Redlaunch generates the pair, installs the
public key in the dedicated `redlaunch` user authorized keys file, and shows
the private key once. Revoking a key removes its public key from
`authorized_keys`.

Keys grant shell access as the dedicated `redlaunch` user. That user owns no
Redlaunch files and has no sudo privileges. Treat keys like host credentials.

## Restricting a key to one service

The creation form offers an optional service restriction. A restricted key is
tunnel-only: its `authorized_keys` line carries
`no-agent-forwarding,no-X11-forwarding,no-pty,no-user-rc,permitopen="127.0.0.1:PORT"`,
so it cannot open a shell and can only forward to the selected service's
published host port. Unrestricted keys keep full shell access.

The target is resolved when the key is created:

- Redis services use their configured port (`127.0.0.1:<port>`).
- Other services use the first TCP host port published in the application's
  `compose.yml` (`127.0.0.1:<host port>`).
- Services that publish no host port (for example Postgres, which is only
  reachable inside its Compose network) cannot be restriction targets; the
  form rejects them with an explanatory error.

The restriction is a snapshot: if the service's published port changes later,
or the service is removed, recreate the key. A stale target fails closed
(the tunnel is refused) and never widens access.

## Prerequisites

`make setup` creates the dedicated user with password login locked:

```sh
id redlaunch
sudo grep "redlaunch-ssh-key:" /home/redlaunch/.ssh/authorized_keys
```

The manager container mounts the host file at the identical path:

```yaml
volumes:
  - "/home/redlaunch/.ssh/authorized_keys:/home/redlaunch/.ssh/authorized_keys"
```

The manager preserves unmanaged lines and comments in the file and only
rewrites lines ending in `redlaunch-ssh-key:<id>`. The file is created with
mode `0600` when missing and must not be a symlink.

Existing installations from before this tab existed need the user once:

```sh
sudo useradd --create-home --shell /bin/bash --user-group redlaunch
sudo install -d -m 700 -o redlaunch -g redlaunch /home/redlaunch/.ssh
sudo touch /home/redlaunch/.ssh/authorized_keys
sudo chown redlaunch:redlaunch /home/redlaunch/.ssh/authorized_keys
sudo chmod 600 /home/redlaunch/.ssh/authorized_keys
sudo passwd -l redlaunch
```

Then restart the stack so the new mount applies:

```sh
docker compose up -d
```

## Create a key

1. Open **Settings → SSH keys**.
2. Enter a display name and select **Create SSH key**. Optionally pick a
   service to restrict the key to tunnel-only access for that service.
3. Copy the private key or use **Download private key**. The key is shown only
   in this response (`Cache-Control: no-store`) and is never stored by
   Redlaunch.

Use the private key from the external system:

```sh
install -m 600 redlaunch-ssh-key-1.key ~/.ssh/redlaunch-key
ssh -i ~/.ssh/redlaunch-key redlaunch@your-server
```

A restricted key shows the exact tunnel command to use, for example:

```sh
ssh -N -L 127.0.0.1:5432:127.0.0.1:5432 -i ~/.ssh/redlaunch-key redlaunch@your-server
```

Use the destination verbatim: the key is limited to that `host:port`.

Verify the installed key on the server:

```sh
grep "redlaunch-ssh-key:" /home/redlaunch/.ssh/authorized_keys
```

## Revoke a key

Select the delete action next to a key and confirm **Revoke key**. Redlaunch
removes the matching `redlaunch-ssh-key:<id>` line and deletes the stored
metadata, then redirects back to **Settings → SSH keys**. Remove the private
key from the external system separately.

## Troubleshooting

- A creation failure leaves no stored metadata; the database row is removed
  when the `authorized_keys` write fails.
- A revoke failure keeps both the file and the metadata unchanged, except when
  the metadata delete fails after the file write, in which case Redlaunch
  restores the previous file contents on a best-effort basis.
