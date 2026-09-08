# GitHub Actions image deployment

Redlaunch can configure a repository-specific GitHub Actions workflow from an
application's **Settings → GitHub Actions deployment** page. The wizard starts
a managed SSH gateway, creates a separate Ed25519 client key for the selected
repository, and gives you the exact GitHub variables, secrets, and workflow
file to add.

The workflow builds the repository's Dockerfile and pushes an image tagged with
the commit SHA. It does not restart or update a Redlaunch service. After a
successful run, use the immutable image reference in the managed service that
should run it.

## Prerequisites

The wizard ensures Redlaunch's Docker Registry component is installed and
started before it provisions the gateway. The registry is bound to the
server's private Docker network and is not exposed publicly. The GitHub
Actions gateway is installed automatically by the wizard under:

```text
projects/core/github-actions-tunnel/
```

The gateway publishes TCP port `2222`. Allow inbound TCP `2222` from GitHub
Actions runners (or from the network policy used by your runners). The gateway
has no shell, no Docker socket, and permits forwarding only to
`registry:5000`.

## Run the wizard

1. Open an application in Redlaunch and select **Settings**.
2. In **GitHub Actions deployment**, select **Set up or manage**.
3. Enter the exact GitHub repository as `owner/repository`.
4. Choose the branch, Dockerfile path, build context, Redlaunch service, and
   registry image repository. Paths must be relative to the repository.
5. Enter the public IP address or DNS name that GitHub Actions will use to
   reach this server.
6. Select **Create SSH tunnel and workflow**.

The result page is deliberately `no-store`. Copy the private key and the
known-hosts entry before leaving the page. Redlaunch does not store the client
private key on disk; the completed setup job keeps the handoff in memory for up
to one hour so you can reopen it if the progress page is closed. A Redlaunch
restart clears the in-memory handoff; run setup again in that case (or after
the job expires) to rotate the key and create a new handoff.

The displayed host-key fingerprint is the fingerprint of the gateway's
persistent Ed25519 host key. Verify it through a trusted channel before adding
the known-hosts entry to GitHub.

From a trusted shell on the Redlaunch server (using your configured projects
root), you can verify the displayed value with:

```sh
ssh-keygen -lf projects/core/github-actions-tunnel/host_key.pub
```

## Configure the GitHub repository

In the selected repository, open **Settings → Secrets and variables → Actions**.
Create these repository variables:

| Variable | Value |
| --- | --- |
| `REDLAUNCH_SERVER_HOST` | The public server host entered in the wizard |
| `REDLAUNCH_SERVER_USERNAME` | `redlaunch-deploy` |
| `REDLAUNCH_SERVER_SSH_PORT` | `2222` |

Create these repository secrets with the complete values shown by Redlaunch:

| Secret | Value |
| --- | --- |
| `REDLAUNCH_DEPLOY_SSH_KEY` | The complete Ed25519 private key, including the `BEGIN` and `END` lines |
| `REDLAUNCH_DEPLOY_KNOWN_HOSTS` | The verified gateway host-key entry |

Save the downloaded workflow as:

```text
.github/workflows/redlaunch-push-image.yml
```

Commit it to the selected branch and push. The workflow also includes
`workflow_dispatch`, so you can run it manually from the repository's
**Actions** tab.

## Workflow behavior

The generated workflow:

- checks out the repository with read-only contents permission;
- builds the configured Dockerfile and context;
- writes the SSH key and known-hosts values only under `$RUNNER_TEMP`;
- uses strict host-key checking and `ExitOnForwardFailure`;
- forwards the runner's `localhost:5000` to `registry:5000` through the gateway;
- retries the registry readiness check; and
- closes the tunnel with a shell `trap` after `docker push` completes.

The image reference is:

```text
localhost:5000/<image-repository>:<commit-sha>
```

Set the selected Redlaunch service to the exact commit-SHA image when you are
ready to deploy it. Updating or restarting that service automatically is not
part of this workflow.

## Rotation and revocation

The wizard keeps one public key per application/repository integration. Running
setup again replaces that key and renders a new private-key handoff. **Revoke
repository key** removes the key from the gateway and deletes the integration
metadata. It does not delete the GitHub repository's variables or secrets, so
remove those separately in GitHub.

The registry currently has no namespace-level authorization. A repository key
can therefore push to any repository path in this local registry, although it
cannot obtain a shell or access Docker. Add registry authentication and
authorization before treating separate repositories as mutually untrusted.

## Troubleshooting

- A missing `REDLAUNCH_SERVER_*` value means the repository variable was not
  created at repository scope.
- A host-key error means the known-hosts entry does not match the configured
  host and port. Re-check the fingerprint; do not disable strict checking.
- A connection timeout normally means TCP `2222` is blocked by the VPS
  firewall, cloud security group, or network policy.
- A registry readiness failure means the local Registry component is stopped or
  the gateway could not reach the `redlaunch-registry` Docker network.
- The workflow never updates a running service. Use the SHA-tagged image in
  Redlaunch after the push succeeds.

## References

- [GitHub Actions variables](https://docs.github.com/en/actions/concepts/workflows-and-actions/variables)
- [Using secrets in GitHub Actions](https://docs.github.com/en/actions/how-tos/write-workflows/choose-what-workflows-do/use-secrets)
- [Docker daemon insecure registries](https://docs.docker.com/reference/cli/dockerd/#insecure-registries)
- [Docker Compose push](https://docs.docker.com/reference/cli/docker/compose/push/)
