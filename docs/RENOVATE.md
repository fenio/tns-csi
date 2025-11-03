# Renovate Integration

This repository uses [Renovate](https://docs.renovatebot.com/) to automatically keep dependencies up-to-date.

## What Renovate Monitors

Renovate watches the following dependency types in this project:

1. **Go modules** (`go.mod`) - All Go dependencies including:
   - kubernetes.io/klog
   - google.golang.org/grpc
   - github.com/gorilla/websocket
   - CSI spec dependencies

2. **Docker base images** (`Dockerfile`):
   - `golang:1.25.3-alpine` (builder)
   - `alpine:3.22` (runtime)

3. **GitHub Actions** (`.github/workflows/*.yml`):
   - actions/checkout
   - actions/setup-go
   - Other workflow dependencies

4. **Helm charts** (`charts/tns-csi-driver/`):
   - Chart dependencies (if any are added)

## Configuration

The configuration is in `renovate.json` and includes:

- **Weekly schedule**: Updates run before 6am on Mondays (America/New_York timezone)
- **Grouped updates**: Related dependencies are grouped together (e.g., all Go modules)
- **Auto-merge**: Minor and patch Go module updates are auto-merged after CI passes
- **Manual approval required for**:
  - Major version updates
  - Kubernetes API version changes
- **Security alerts**: High-priority with `security` label and assignee notification
- **Rate limiting**: Max 5 PRs at once, 2 per hour

## Enabling Renovate

### Option 1: GitHub App (Recommended)

1. Install the [Renovate GitHub App](https://github.com/apps/renovate) on your repository
2. Grant it access to `bfenski/tns-csi` (or your fork)
3. Renovate will automatically detect `renovate.json` and start creating PRs

### Option 2: Self-Hosted Renovate

If you prefer to run Renovate on your self-hosted infrastructure:

```bash
# Using Docker
docker run --rm \
  -e RENOVATE_TOKEN="${GITHUB_TOKEN}" \
  -e RENOVATE_REPOSITORIES="bfenski/tns-csi" \
  renovate/renovate:latest

# Or using npm
npm install -g renovate
renovate --token="${GITHUB_TOKEN}" bfenski/tns-csi
```

## How It Works

1. **Dependency Detection**: Renovate scans the repository for dependency files
2. **Update Check**: Checks for newer versions of dependencies
3. **PR Creation**: Creates pull requests with updates
4. **CI Validation**: GitHub Actions runs tests automatically
5. **Auto-merge**: Minor/patch updates merge automatically if CI passes
6. **Dashboard**: View all pending updates in the Dependency Dashboard issue

## Renovate Pull Request Flow

When Renovate creates a PR:

1. ✅ **CI runs automatically** - Both NFS and NVMe-oF integration tests
2. ✅ **Auto-merge eligible** - Minor/patch Go updates merge if tests pass
3. ⚠️ **Manual review required** - Major updates wait in Dependency Dashboard
4. 🔒 **Security updates** - Labeled and assigned immediately

## Dependency Dashboard

Renovate creates a "Dependency Dashboard" issue that shows:
- All pending updates
- Updates waiting for approval
- Rate-limited updates
- Any errors encountered

Check: https://github.com/bfenski/tns-csi/issues (look for "Dependency Dashboard")

## Customizing Renovate

To modify Renovate behavior, edit `renovate.json`:

```json
{
  "schedule": ["before 6am on monday"],  // Change update schedule
  "prConcurrentLimit": 5,                 // Max concurrent PRs
  "automerge": true,                      // Enable/disable auto-merge
  "labels": ["dependencies"]              // PR labels
}
```

After changes, Renovate will pick up the new configuration on the next run.

## Testing Renovate Configuration

Validate your `renovate.json` before committing:

```bash
# Using Renovate's config validator
npx -p renovate -c 'renovate-config-validator'

# Or use the online validator:
# https://docs.renovatebot.com/config-validator/
```

## Troubleshooting

**Q: Renovate isn't creating PRs**
- Check the Dependency Dashboard issue for rate limits or errors
- Verify Renovate has write access to the repository
- Check the Renovate logs (if self-hosted)

**Q: Too many PRs at once**
- Adjust `prConcurrentLimit` in `renovate.json`
- Use `groupName` to combine related updates

**Q: Update broke CI**
- Renovate won't auto-merge if CI fails
- Review the failed PR and fix or close it
- Consider adding the package to `dependencyDashboardApproval`

## Disabling Renovate

To temporarily disable Renovate:

1. **For specific dependencies**: Add to `ignoreDeps` in `renovate.json`
2. **For specific packages**: Add `"enabled": false` to a packageRule
3. **Completely**: Remove the Renovate GitHub App or delete `renovate.json`

## References

- [Renovate Documentation](https://docs.renovatebot.com/)
- [Configuration Options](https://docs.renovatebot.com/configuration-options/)
- [Go Modules Support](https://docs.renovatebot.com/modules/manager/gomod/)
- [GitHub Actions Support](https://docs.renovatebot.com/modules/manager/github-actions/)
