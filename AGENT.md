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

```
 ssh / Remote-SSH --stdio--> cli.connect
                              |-- kube.Load                kubeconfig, context
                              |-- kube.ResolveCredential   exec plugin with a deadline
                              |-- kube.CreateConnection    POST WorkspaceConnection -> wss URL + token
                              |-- tunnel.Dial              WebSocket, token in the Authorization header
                              '-- tunnel.Relay             stdin -> frames, frames -> stdout
```

- `internal/cli`: the three commands on cobra. `connect` is the ProxyCommand,
  `ssh-config` the one-time setup, `open` launches VS Code. `root.go` holds the
  shared flags, the `kubectl workspace` usage prefix and `Main`, which maps signals
  and errors to the exit code.
- `internal/sshconfig`: the alias `<workspace>.<namespace>.<cluster>.jk8s` and the
  per-cluster entry `Host *.<cluster>.jk8s`, rendered between marker comments and
  installed into `~/.ssh/config`.
- `internal/kube`: kubeconfig loading with client-go's `clientcmd`, the exec
  credential plugin run under a deadline, and the `WorkspaceConnection` request to
  the operator's extension API.
- `internal/tunnel`: the WebSocket dial, verifying TLS against the system roots or the
  `--ca-file` bundle, and the stdio relay.
- `internal/testutil`: the fakes the tests share: an operator answering connection
  requests over TLS, a WebSocket echo server standing in for the sidecar, a
  kubeconfig writer, an HTTP CONNECT proxy and a listener that never answers.

## Invariants

- stdout is the SSH byte stream. `connect` runs under `ssh`; any other byte on
  stdout corrupts the session. Every diagnostic goes to stderr, prefixed
  `kubectl-workspace:`.
- The credential plugin never sees the tunnel. client-go would run the kubeconfig's
  exec plugin on the first request, with no deadline and with the process's stdin,
  which under `ssh` is the SSH stream. `kube.ResolveCredential` runs it itself: no
  stdin, `interactive: false` in `KUBERNETES_EXEC_INFO`, its own process group,
  SIGTERM at the deadline. A plugin that produces nothing before the deadline is a
  `SignInRequiredError`, one that exits non-zero a `PluginExitError`; both get the
  `kubectl workspace open` hint, a missing plugin gets the kubeconfig's install hint
  instead.
- A signal ends the session quietly. OpenSSH ends a ProxyCommand with SIGHUP.
  `connect` exits 0 with nothing printed whichever phase the signal interrupts; the
  dial watches the context itself, because the WebSocket library stops watching it
  once the TCP connection is up.
- The token travels in a header. The operator returns `wss://...?token=`; the auth
  middleware also reads `Authorization: Bearer`, and a header stays out of access
  logs. The token is never logged, including under `-v`.
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
| `cmd/kubectl-workspace/` | Entry point; imports client-go's auth providers |
| `internal/cli/` | `connect`, `ssh-config`, `open`, shared flags, `Main` |
| `internal/kube/` | Kubeconfig loading, exec credential plugin with a deadline, `WorkspaceConnection` creation |
| `internal/sshconfig/` | Alias parsing, entry rendering and installation |
| `internal/tunnel/` | WebSocket dial with the token in a header, stdin/stdout relay |
| `internal/testutil/` | Fakes shared by the tests |
| `test/e2e/` | The system's `ssh` and `sftp` through the plugin against the published sidecar and SSH server images (`e2e` build tag) |
| `test/e2e/kind/` | The plugin against a Kind cluster with the operator, the auth middleware and Traefik (`e2e` build tag) |
| `test/e2e/internal/harness/` | Helpers both end-to-end suites share |

## Development

### Prerequisites

- Go 1.26+
- golangci-lint v2.12+

### Common Tasks

- Build: `make build`
- Tests: `make test`
- End to end against the published images: `make test-e2e`, with `CONTAINER_TOOL=finch`
  on a Mac
- End to end on Kind: `make test-e2e-kind-full`, or `make setup-e2e-kind` once and
  `make test-e2e-kind` per run, with `CONTAINER_TOOL=finch` on a Mac
- Lint: `make lint`, with fixes `make lint-fix`
- Dependencies: `make deps`
- Run against a sidecar without a cluster: `bin/kubectl-workspace connect --stdio
  --url ws://127.0.0.1:18080/ <alias>` (`--url` is hidden from `--help`; it skips the
  operator, and a `token` query parameter, if present, travels in the `Authorization`
  header as the operator's token would)

### Tests

Every package tests its contract against the fakes in `internal/testutil`; nothing
needs a cluster, `ssh` or `kubectl`. `internal/kube` re-executes the test binary as
the exec credential plugin (`KUBE_TEST_FAKE_PLUGIN`), so the deadline, the process
group and the `KUBERNETES_EXEC_INFO` contract run against a real subprocess.
`internal/cli/integration_test.go` runs the plugin itself as a subprocess in the
ProxyCommand position, against the echo server, and checks the exit code and the
silence on stderr when the client closes and on SIGHUP.

`test/e2e` runs the real artifacts: the published sidecar image and the SSH server
from the published carrier image, in two containers sharing one network namespace
as they share a pod's, with the system's `ssh` and `sftp` running the plugin as the
ProxyCommand through `--url`. It proves a command, its exit status, a terminal, a
4 MiB transfer each way and an SFTP round trip. The operator is not in that rig; the
Kind suite covers it.

`test/e2e/kind` runs the plugin the way a deployment does: a Kind cluster with the
operator chart from GHCR and token signing on, the auth middleware with bearer auth,
and Traefik serving a certificate from a certificate authority of the rig's own, whose
ForwardAuth middleware hands the connection's token to the auth middleware before the
WebSocket reaches the published sidecar. The suite writes the SSH entry with
`ssh-config --write --ca-file` against the cluster's kubeconfig, reaches Traefik's TLS
port through a `kubectl port-forward`, and checks a command, the exit status, an SFTP
round trip, `open --print-only`, and the refusals without the rig's CA, for a service
account without access and for a stopped workspace. `make setup-e2e-kind` installs the
cluster-level pieces at the versions pinned in the Makefile; the suite applies
`test/e2e/testdata/kind/fixtures.yaml`.

When adding a test:

- It sits next to the code it pins, in the package's own `_test.go`, and asserts the
  contract (the output for an input, the file written, the error returned), not the
  call sequence that produces it.
- A fake another package will also need goes in `internal/testutil`; a fake one test
  needs stays in that test.
- Nothing in the unit tests needs a cluster, `ssh` or `kubectl`; a check that needs
  the real `ssh` belongs in `test/e2e`, one that needs the operator in `test/e2e/kind`.

### Before Submitting a PR

- `make build`
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
- The command is `kubectl workspace`; usage and version output carry the `kubectl`
  prefix whether the binary is invoked through kubectl or directly.
- A lint finding is fixed rather than silenced; a `//nolint` directive needs the
  user's agreement first.
