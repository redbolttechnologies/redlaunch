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
2. Enter a display name and select **Create SSH key**.
3. Copy the private key or use **Download private key**. The key is shown only
   in this response (`Cache-Control: no-store`) and is never stored by
   Redlaunch.

Use the private key from the external system:

```sh
install -m 600 redlaunch-ssh-key-1.key ~/.ssh/redlaunch-key
ssh -i ~/.ssh/redlaunch-key redlaunch@your-server
```

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
