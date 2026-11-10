# kubectl-workspace Developer Guide

> Note: `CLAUDE.md` is a symlink to this file (`AGENT.md`).
> Always edit `AGENT.md` directly, never `CLAUDE.md`.

## Project Overview

The kubectl plugin that runs on the user's machine as OpenSSH's `ProxyCommand` and
carries one SSH connection to a [jupyter-k8s](https://github.com/jupyter-infra/jupyter-k8s)
workspace: through the cluster's HTTPS ingress as a WebSocket, authorized by the
user's kubeconfig credential, into the `ws-proxy` sidecar from
[workspace-websocket-proxy](https://github.com/jupyter-infra/workspace-websocket-proxy),
which relays to the SSH server in the workspace pod. The design and its decisions are
in [jupyter-infra/jupyter-k8s#509](https://github.com/jupyter-infra/jupyter-k8s/issues/509).

Module path: `github.com/jupyter-infra/kubectl-workspace`

No dependency on the operator module or on the proxy module: the operator's request
and response types are mirrored in three small structs in `internal/kube`, which keeps
the operator's dependency graph out of this binary.

## Architecture

The plugin is built in layers that land in this order; the commands in `internal/cli`
are the one layer still to come:

```
 ssh / Remote-SSH --stdio--> cli.connect
                              |-- kube.Load                kubeconfig, context
                              |-- kube.ResolveCredential   exec plugin with a deadline
                              |-- kube.CreateConnection    POST WorkspaceConnection -> wss URL + token
                              |-- tunnel.Dial              WebSocket, token in the Authorization header
                              '-- tunnel.Relay             stdin -> frames, frames -> stdout
```

- `internal/sshconfig`: the alias `<workspace>.<namespace>.<cluster>.jk8s` and the
  per-cluster entry `Host *.<cluster>.jk8s`, rendered between marker comments and
  installed into `~/.ssh/config`.
- `internal/kube`: kubeconfig loading with client-go's `clientcmd`, the exec
  credential plugin run under a deadline, and the `WorkspaceConnection` request to
  the operator's extension API.
- `internal/tunnel`: the WebSocket dial and the stdio relay.
- `internal/testutil`: the fakes the tests share: an operator answering connection
  requests over TLS, a WebSocket echo server standing in for the sidecar, a
  kubeconfig writer, an HTTP CONNECT proxy and a listener that never answers.

## Invariants

- The credential plugin never sees the tunnel. client-go would run the kubeconfig's
  exec plugin on the first request, with no deadline and with the process's stdin,
  which under `ssh` is the SSH stream. `kube.ResolveCredential` runs it itself: no
  stdin, `interactive: false` in `KUBERNETES_EXEC_INFO`, its own process group,
  SIGTERM at the deadline. A plugin that produces nothing before the deadline is a
  `SignInRequiredError`, one that exits non-zero a `PluginExitError`, so the caller
  can tell a pending sign-in from a failure; a missing plugin carries the
  kubeconfig's install hint.
- The dial watches the context itself, because the WebSocket library stops watching
  it once the TCP connection is up; a cancelled context ends the dial and the relay
  without an error of their own.
- The token travels in a header. The operator returns `wss://...?token=`; the auth
  middleware also reads `Authorization: Bearer`, and a header stays out of access
  logs. The token is never logged.
- The alias is parsed from the right. The suffix, the cluster and the namespace are
  single labels; the workspace name may contain dots. The kubeconfig context is not
  in the alias, because EKS context names contain `:` and `/`; the entry carries it,
  and the kubeconfig file when the user chose one.
- The entry goes before the first `Host` or `Match` block and the comment lines directly
  above it, and after everything else above them. A `Host` block runs to the next `Host`
  or `Match`, so an entry placed above a top-level `Include` would capture the included
  options for its own hosts. Another cluster's entry counts from its start marker, so a
  second cluster lands before it, not inside its markers.
- The entry is one ssh_config line per option. Arguments are quoted for the shell,
  `%` is doubled because OpenSSH expands percent tokens on the ProxyCommand line, and
  the `User` value is one token, because OpenSSH aborts on a second one.

## Repository Layout

| Directory / file | Purpose |
|------------------|---------|
| `internal/kube/` | Kubeconfig loading, exec credential plugin with a deadline, `WorkspaceConnection` creation |
| `internal/sshconfig/` | Alias parsing, entry rendering and installation |
| `internal/tunnel/` | WebSocket dial with the token in a header, stdin/stdout relay |
| `internal/testutil/` | Fakes shared by the tests |

## Development

### Prerequisites

- Go 1.26+
- golangci-lint v2.12+

### Common Tasks

- Tests: `make test`
- Lint: `make lint`, with fixes `make lint-fix`
- Dependencies: `make deps`

### Tests

Every package tests its contract against the fakes in `internal/testutil`; nothing
needs a cluster, `ssh` or `kubectl`. `internal/kube` re-executes the test binary as
the exec credential plugin (`KUBE_TEST_FAKE_PLUGIN`), so the deadline, the process
group and the `KUBERNETES_EXEC_INFO` contract run against a real subprocess.

When adding a test:

- It sits next to the code it pins, in the package's own `_test.go`, and asserts the
  contract (the output for an input, the file written, the error returned), not the
  call sequence that produces it.
- A fake another package will also need goes in `internal/testutil`; a fake one test
  needs stays in that test.
- Nothing in the unit tests needs a cluster, `ssh` or `kubectl`.

### Before Submitting a PR

- `make lint`
- `make test`

Anything touching signals or platform-specific syscalls is also checked on Linux, not
only on a developer Mac; the test workflow runs both. Windows is cross-compiled, not run.

## Automated code review

Before asking for a review, run roborev on the branch: `make review` runs
`roborev review --branch --local --wait` with whatever coding agent is installed and
prints the findings. Install roborev from https://roborev.io. jupyter-k8s, jupyter-k8s-ui
and jupyter-deploy also run it in CI on every pull request; this repository does not.

## CI & Release

See [`.github/AGENT.md`](.github/AGENT.md) for the workflows.

## Notes

- Copyright header: `Amazon Web Services`
- golangci-lint v2 (see `.golangci.yml`): `revive`'s `exported` rule requires a doc
  comment on exported declarations; `lll` at 120 columns; `goconst` at three
  occurrences.
- A lint finding is fixed rather than silenced; a `//nolint` directive needs the
  user's agreement first.
