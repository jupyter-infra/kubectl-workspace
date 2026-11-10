# CI Workflows

## Workflows

| Workflow | Trigger | Purpose |
|----------|---------|---------|
| `lint.yml` | push/PR | golangci-lint |
| `test.yml` | push/PR | A `go mod tidy` drift check, the tests with the race detector on Linux and macOS, and a cross-compile of every release target |
| `e2e.yml` | push/PR | `ssh` and `sftp` through the plugin against the published sidecar and SSH server images, in containers on the runner |

## Testing workflow changes

All three run on every pull request, so a change to them is tested by the PR that
carries it. The end-to-end rig pulls `ghcr.io/jupyter-infra/workspace-websocket-proxy`
and `ghcr.io/jupyter-infra/remote-access-server` at the tag pinned in
`test/e2e/plugin_test.go` and `test/e2e/testdata/workspace.Dockerfile`; bump both
together.
