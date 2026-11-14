# CI Workflows

## Workflows

| Workflow | Trigger | Purpose |
|----------|---------|---------|
| `lint.yml` | push to main, PR, manual | golangci-lint |
| `test.yml` | push to main, PR, manual | A `go mod tidy` drift check, the tests with the race detector on Linux, macOS and Windows, and a cross-compile of every release target |
| `e2e.yml` | push to main, PR, manual | Three jobs: `ssh` and `sftp` through the plugin against the published sidecar and SSH server images in containers on the runner; the operating system's OpenSSH on Ubuntu, macOS and Windows through the entry the plugin writes, against an in-process SSH server; and `ssh` through a Kind cluster with the operator, the auth middleware and Traefik, once on the oldest supported operator release and once on the latest |

## Testing workflow changes

All three run on every pull request, so a change to them is tested by the PR that
carries it. The Makefile pins what the end-to-end rigs run against: the sidecar image
and the SSH server image (`E2E_SIDECAR_IMAGE`, `E2E_SSH_SERVER_IMAGE`, bumped
together) and the cert-manager and Traefik chart versions. The Kind job lists the
operator versions it runs on in its matrix; the auth middleware image in
`test/e2e/testdata/kind/authmiddleware.yaml` takes the operator version when the
Makefile applies it. `e2e.yml` pins the kind binary. `lint.yml` pins the golangci-lint
version; `.golangci.yml` is in its v2 format.
