# Setup k3s Action

A GitHub Action for installing and configuring [k3s](https://k3s.io) - a lightweight, certified Kubernetes distribution perfect for CI/CD pipelines, testing, and development workflows.

## Features

- ✅ Automatic installation of k3s
- ✅ Cleans up conflicting Kubernetes installations (k0s, minikube, KubeSolo)
- ✅ Waits for cluster readiness (nodes and system pods)
- ✅ Outputs kubeconfig path for easy integration
- ✅ **Automatic cleanup and system restoration** - Runs post-cleanup after your workflow completes
- ✅ Configurable k3s arguments for customization

## Quick Start

```yaml
name: Test with k3s

on: [push]

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      
      - name: Setup k3s
        id: k3s
        uses: fenio/setup-k3s@v1
      
      - name: Deploy and test
        env:
          KUBECONFIG: ${{ steps.k3s.outputs.kubeconfig }}
        run: |
          kubectl apply -f k8s/
          kubectl wait --for=condition=available --timeout=60s deployment/my-app
      
      # Cleanup happens automatically after this job completes!
```

## Inputs

| Input | Description | Default |
|-------|-------------|---------|
| `version` | k3s version to install (e.g., `v1.28.5+k3s1`, `latest`, `stable`) | `stable` |
| `k3s-args` | Additional arguments to pass to k3s installer | `--write-kubeconfig-mode 644` |
| `wait-for-ready` | Wait for cluster to be ready before completing | `true` |
| `timeout` | Timeout in seconds to wait for cluster readiness | `120` |

## Outputs

| Output | Description |
|--------|-------------|
| `kubeconfig` | Path to the kubeconfig file (`/etc/rancher/k3s/k3s.yaml`) |

## Examples

### Basic Usage

```yaml
- name: Setup k3s
  uses: fenio/setup-k3s@v1
```

### Specific Version

```yaml
- name: Setup k3s v1.28.5
  uses: fenio/setup-k3s@v1
  with:
    version: v1.28.5+k3s1
```

### Disable Components

```yaml
- name: Setup k3s (minimal)
  uses: fenio/setup-k3s@v1
  with:
    k3s-args: '--write-kubeconfig-mode 644 --disable=traefik --disable=servicelb'
```

### Latest Version

```yaml
- name: Setup k3s (latest)
  uses: fenio/setup-k3s@v1
  with:
    version: latest
```

## How It Works

### Setup Phase
1. Cleans up any existing Kubernetes installations (k0s, minikube, KubeSolo, old k3s)
2. Waits for port 6443 to be free
3. Installs k3s using the official installation script
4. Waits for the cluster to become ready (nodes Ready, system pods Running)
5. Exports `KUBECONFIG` environment variable

### Automatic Cleanup (Post-run)
After your workflow steps complete (whether successful or failed), the action automatically:
1. Stops and uninstalls k3s using the official uninstall script
2. Removes k3s configuration files and data directories
3. Leaves your system in a clean state

This is achieved using GitHub Actions' `post:` hook, similar to how `actions/checkout` cleans up after itself.

## Requirements

- Runs on `ubuntu-latest` (or any Linux-based runner)
- Requires `sudo` access (provided by default in GitHub Actions)

## Version Selection

The `version` input accepts:
- **`stable`** (default) - Latest stable release channel
- **`latest`** - Latest release (including pre-releases)
- **Specific version** - e.g., `v1.28.5+k3s1` (see [k3s releases](https://github.com/k3s-io/k3s/releases))

## Troubleshooting

### Cluster not becoming ready

If the cluster doesn't become ready in time, increase the timeout:

```yaml
- name: Setup k3s
  uses: fenio/setup-k3s@v1
  with:
    timeout: 300  # 5 minutes
```

### Custom k3s arguments

To pass custom arguments to k3s:

```yaml
- name: Setup k3s
  uses: fenio/setup-k3s@v1
  with:
    k3s-args: |
      --write-kubeconfig-mode 644
      --disable=traefik
      --disable=servicelb
      --kube-apiserver-arg=feature-gates=EphemeralContainers=true
```

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## License

MIT License - see [LICENSE](LICENSE) file for details.

## Related Actions

- [fenio/setup-kubesolo](https://github.com/fenio/setup-kubesolo) - Ultra-lightweight Kubernetes for CI/CD
- [fenio/setup-k0s](https://github.com/fenio/setup-k0s) - Zero friction Kubernetes (coming soon)
- [fenio/setup-minikube](https://github.com/fenio/setup-minikube) - Local Kubernetes (coming soon)
