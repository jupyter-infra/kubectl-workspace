# kubectl-workspace

`kubectl workspace` connects your machine to a
[jupyter-k8s](https://github.com/jupyter-infra/jupyter-k8s) workspace over SSH.
OpenSSH runs it as the `ProxyCommand`; it asks the operator for a
connection with your kubeconfig credential, opens a WebSocket through the cluster's
HTTPS ingress to the workspace's sidecar and relays the SSH bytes. The same alias
serves `ssh`, `sftp` and VS Code's Remote-SSH. No SSH key is created or
distributed: your Kubernetes identity is the credential.

## Install

`make build` writes `bin/kubectl-workspace`; copy it onto `PATH`, where `kubectl`
finds it as `kubectl workspace`. Release archives and a krew manifest will come with
the first release.

## Set up a cluster

`kubectl` has to be configured for the cluster, the way the web UI's Kubectl Access
page sets it up. Then, once per cluster:

```
kubectl workspace ssh-config --write --context <context>
```

It adds one entry to `~/.ssh/config`:

```
# kubectl-workspace: start <cluster>
Host *.<cluster>.jk8s
  User jovyan
  ProxyCommand /path/to/kubectl-workspace connect --stdio --context <context> %h
  ServerAliveInterval 30
  StrictHostKeyChecking accept-new
# kubectl-workspace: end <cluster>
```

`<cluster>` is the context name; pass `--cluster-name` when the context is named
like an EKS ARN, which cannot be part of a host name. Running the command again
replaces the entry. From then on every workspace in the cluster is an SSH host:

```
ssh <workspace>.<namespace>.<cluster>.jk8s
sftp <workspace>.<namespace>.<cluster>.jk8s
kubectl workspace open <workspace>
```

`open` launches a desktop editor on the workspace through VS Code's Remote-SSH link:
VS Code by default, or Cursor with `--editor cursor`. Either editor needs its Remote-SSH
extension; Cursor's is `anysphere.remote-ssh`, which a fresh install does not include.

## How it works

```
 ssh, sftp, VS Code Remote-SSH
   |  ProxyCommand: kubectl workspace connect --stdio <workspace>.<namespace>.<cluster>.jk8s
   v
 kubectl-workspace --1--> operator: create a WorkspaceConnection with the kubeconfig credential
                   <--2-- wss://<ingress>/workspaces/<namespace>/<workspace>/ssh-ws?token=<5-minute token>
                   --3--> ingress: WebSocket with the token in the Authorization header
                            |  the auth middleware verifies the token
                            v
                          ws-proxy sidecar --> remote-access-server (SSH) inside the workspace pod
```

1. `connect` parses the alias, loads the kubeconfig context named in the entry and
   resolves its credential, running an exec credential plugin itself when the
   kubeconfig has one.
2. It asks the operator's extension API for an `ssh-over-websocket` connection. The
   operator checks that the user may create connections in the namespace and reach
   the workspace, that the workspace is available and that its access strategy has
   the WebSocket route, then mints a token that lives five minutes.
3. It opens the WebSocket to the ingress with the token in the `Authorization`
   header, where it stays out of access logs, and relays bytes between `ssh` and the
   sidecar until `ssh` exits or the sidecar closes the session.

Only the SSH stream reaches stdout; every diagnostic goes to stderr, which `ssh -v`
and the Remote-SSH log show. A session that ends normally exits 0 with nothing
printed; a close the sidecar explains, such as its session duration limit, is
printed with its reason.

## Commands

Global flags, on every command:

| Flag | Default | Purpose |
|------|---------|---------|
| `--kubeconfig` | `$KUBECONFIG` or `~/.kube/config` | Kubeconfig file |
| `--context` | the current context | Kubeconfig context |
| `-v`, `--verbose` | off | Progress lines on stderr |

`kubectl workspace connect --stdio <alias>`, the ProxyCommand:

| Flag | Default | Purpose |
|------|---------|---------|
| `--stdio` | required | Relay SSH between stdin/stdout and the workspace |
| `--credential-timeout` | `5s` | How long the kubeconfig's credential plugin may take before `connect` fails |
| `--ca-file` | system roots | PEM bundle of the ingress certificate authority, see Private certificate authorities |
| `--insecure-allow-plaintext` | off | Accept a connection URL without TLS from the operator, for a local cluster; the token then travels in plaintext. The operator today always returns `wss` |

`kubectl workspace ssh-config [--write]`, the one-time setup:

| Flag | Default | Purpose |
|------|---------|---------|
| `--write` | off | Install the entry into the SSH configuration file; without it the entry is printed |
| `--cluster-name` | the context name | The `<cluster>` label of the aliases |
| `--ssh-config-file` | `~/.ssh/config` | The file to install into |
| `--command` | this binary | Path of `kubectl-workspace` for the ProxyCommand |
| `--user` | `jovyan` | SSH user name in the entry |
| `--credential-timeout` | `5s` | Written into the entry when it differs from the default |
| `--ca-file` | system roots | Written into the entry as `connect`'s `--ca-file` |
| `--insecure-allow-plaintext` | off | Written into the entry as `connect`'s `--insecure-allow-plaintext` |

`kubectl workspace open <workspace>`, launches the editor:

| Flag | Default | Purpose |
|------|---------|---------|
| `-n`, `--namespace` | the context's | Namespace of the workspace |
| `--cluster-name` | the context name | The `<cluster>` label of the alias |
| `--remote-path` | `/home/jovyan` | Folder to open in the workspace |
| `--ssh-config-file` | `~/.ssh/config` | The file that must hold the cluster's entry |
| `--editor` | `vscode` | Editor to open the workspace in: `vscode` or `cursor` |
| `--print-only` | off | Print the link and the `ssh` command instead of launching |

## Credentials and signing in

When the kubeconfig credential comes from a plugin such as kubelogin and has
expired, the plugin waits for a browser sign-in. Inside an SSH connection no browser
sign-in can complete, so `connect` gives the plugin five seconds, stops it and fails
with the command to run instead:

```
kubectl workspace open <workspace>
```

kubelogin would also open a browser tab on every attempt, so `connect` runs it with
`--skip-open-browser` and the sign-in URL goes to stderr instead.

`open` runs the sign-in from the terminal, checks the connection with the operator
and launches the editor. A plugin that exits with an error gets the same hint; one that
is not installed gets the kubeconfig's install hint. A plugin that is merely slow
needs a longer deadline in the entry. `--write` replaces the cluster's entry, so repeat
the other flags of your original command, such as `--ca-file` or `--user`:

```
kubectl workspace ssh-config --write --context <context> --credential-timeout 15s
```

## Host keys

Host keys are trusted on first use (`StrictHostKeyChecking accept-new`). A workspace
that was deleted and recreated has a new key, which OpenSSH refuses until you run
`ssh-keygen -R <workspace>.<namespace>.<cluster>.jk8s`.

## Private certificate authorities

The plugin verifies the ingress certificate against the system roots. When the
certificate comes from a private certificate authority, pass its PEM bundle to
`kubectl workspace ssh-config --write --ca-file <bundle>`; the entry carries the path
and `connect` verifies against that bundle instead.

## Requirements

On your machine: OpenSSH 7.6 or later; the entry's `StrictHostKeyChecking accept-new`
arrived in that release. In the cluster:

| kubectl-workspace | jupyter-k8s operator and auth middleware | ws-proxy sidecar |
|-------------------|------------------------------------------|------------------|
| v0.1 | v0.3.0 or later, with token signing enabled in the chart (`extensionApi.jwtSecret.enable`) | v0.1.0-rc.2 or later |

The workspace's access strategy has to carry the WebSocket route
(`webSocketURLTemplate`) and the sidecar from
[workspace-websocket-proxy](https://github.com/jupyter-infra/workspace-websocket-proxy).
A workspace whose strategy lacks the route is refused with the operator's message.

## Troubleshooting

Run `ssh -v <alias>`: the plugin's messages appear in the output, prefixed
`kubectl-workspace:`. In VS Code they appear in the Remote-SSH output panel. Add
`-v` to the ProxyCommand in the entry for progress lines.

## Development

`make build`, `make test` and `make lint` are the local checks. [AGENT.md](AGENT.md)
describes the packages, their invariants, the tests and the two end-to-end rigs;
[.github/AGENT.md](.github/AGENT.md) describes the workflows.

## License

MIT, see [LICENSE](LICENSE).
