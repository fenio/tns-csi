# GitHub Actions workflows

| Workflow | Runner | Trigger | Purpose |
|---|---|---|---|
| `ci.yml` | `ubuntu-latest` | push, PR, dispatch | Helm render checks, golangci-lint, unit tests (`make test-unit`), govulncheck, image build (push on branches, build-only on PRs), kubectl plugin build |
| `sanity.yml` | `ubuntu-latest` | push to main, PR (driver paths), dispatch | CSI specification compliance tests (`make test-sanity`) |
| `integration.yml` | `ubuntu-26.04` (QEMU + k3s VM via `.github/actions/qemu-vm`) | `workflow_run` after CI/Release on `main`, dispatch (with `pr` input) | Full E2E suite: NFS, NVMe-oF, iSCSI, SMB, Shared. Does not run automatically on PRs; dispatch it with the PR number to test a branch |
| `qemu-e2e.yml` | `ubuntu-26.04` (QEMU + k3s VM) | dispatch | NFS-only QEMU smoke test (predates the per-protocol split in `integration.yml`; kept as a minimal repro path) |
| `release.yml` | `ubuntu-latest` | tag push | Build & push multi-arch image, publish Helm chart |
| `release-plugin.yml` | `ubuntu-latest` | tag push | Build & release the kubectl plugin |
| `dashboard.yml` | `ubuntu-latest` | schedule, push | Generate the test results dashboard |
| `sonarqube.yml` | `ubuntu-latest` | push, PR | SonarQube analysis |

## Removed workflows

Seven workflows that depended on a retired self-hosted runner (`encryption`, `scale`,
`snapclone-stress`, `snapshot-clone-matrix`, `snapshot-debug`, `compatibility`,
`distro-compatibility`) were kept for a while as `*.yml.disabled` files and have since been
deleted. They remain in git history (`git log --all -- '.github/workflows/*.disabled'`) if
any of them is ported to the QEMU pattern: restore the file, drop the `.disabled` suffix,
replace `runs-on: new` with `runs-on: ubuntu-26.04`, and add a
`uses: ./.github/actions/qemu-vm` step (see `integration.yml` for the canonical pattern).
