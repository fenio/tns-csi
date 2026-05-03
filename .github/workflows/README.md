# GitHub Actions workflows

## Active

| Workflow | Runner | Trigger | Purpose |
|---|---|---|---|
| `ci.yml` | `ubuntu-latest` | push, PR | Lint, unit tests, sanity tests, build |
| `sanity.yml` | `ubuntu-latest` | push, PR | CSI specification compliance tests |
| `integration.yml` | `ubuntu-24.04` (QEMU + k3s VM via `.github/actions/qemu-vm`) | push to main, PR, dispatch | Full E2E suite: NFS, NVMe-oF, iSCSI, SMB, Shared |
| `qemu-e2e.yml` | `ubuntu-24.04` (QEMU + k3s VM) | dispatch | NFS-only QEMU smoke test (predates the per-protocol split in `integration.yml`; kept as a minimal repro path) |
| `release.yml` | `ubuntu-latest` | tag push | Build & push multi-arch image, publish Helm chart |
| `release-plugin.yml` | `ubuntu-latest` | tag push | Build & release the kubectl plugin |
| `dashboard.yml` | `ubuntu-latest` | schedule, push | Generate the test results dashboard |
| `sonarqube.yml` | `ubuntu-latest` | push | SonarQube analysis |

## Disabled — pending QEMU migration

The following workflows used to run on a self-hosted GitHub Actions runner labelled `new`. That runner has been retired. They are renamed with a `.yml.disabled` suffix so GitHub Actions ignores them; they remain in the repo as a record of intent and so the migration work has a clear inventory.

| File | Jobs | Notes |
|---|---|---|
| `encryption.yml.disabled` | 7 | Per-protocol encryption tests — same shape as `integration.yml`, should port cleanly to the QEMU composite action |
| `scale.yml.disabled` | 2 | Synthetic load against TrueNAS |
| `snapclone-stress.yml.disabled` | 2 | 120-min snapshot/clone stress; was the only auto-firing one (workflow_run after Integration) |
| `snapshot-clone-matrix.yml.disabled` | 13 | Matrix of snapshot/clone scenarios across protocols |
| `snapshot-debug.yml.disabled` | 1 | Single focused snapshot debug run |
| `compatibility.yml.disabled` | 1 | Helm upgrade-compatibility test (old release → new release) |
| `distro-compatibility.yml.disabled` | 19 | K8s distros × protocols matrix (K3s, K0s, KubeSolo, Minikube, Talos, MicroK8s) — hardest to port because each distro needs its own cloud-init installer; will likely need per-distro composite actions |

To re-enable any of these once migrated to the QEMU pattern: rename `.yml.disabled` → `.yml` and replace `runs-on: new` with `runs-on: ubuntu-24.04` plus a `uses: ./.github/actions/qemu-vm` step (see `integration.yml` for the canonical pattern).
