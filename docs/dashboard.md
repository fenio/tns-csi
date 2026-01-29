# TrueNAS CSI Test Results Dashboard

The test results dashboard provides real-time visualization of integration test results for the TrueNAS CSI driver.

## Overview

The dashboard displays:
- Total test counts (passed, failed, skipped)
- Success rate percentage
- Test results by protocol (NFS, NVMe-oF, iSCSI)
- Test results by type (basic, snapshot, concurrent, etc.)
- Recent failures with links to workflow runs

## Viewing the Dashboard

The dashboard is available at: https://fenio.github.io/tns-csi/dashboard/

## How It Works

1. **Data Source**: GitHub Actions workflow runs via GitHub API
2. **Generation**: Node.js script fetches and processes test results from the last 30 days
3. **Deployment**: Automated via GitHub Actions to the `gh-pages` branch
4. **Visualization**: Static HTML with Chart.js for interactive charts

## Automatic Updates

The dashboard is automatically updated:
- After each Integration Tests workflow completes
- Daily at 6:00 AM UTC (to keep data fresh)
- On manual trigger via workflow dispatch

## Local Development

To run the dashboard locally:

```bash
cd dashboard

# Install dependencies
npm install

# Set GitHub token for API access
export GITHUB_TOKEN=your_github_token

# Generate dashboard
npm run build

# Serve locally
npm run dev
# Then open http://localhost:3000
```

## Architecture

```
dashboard/
├── generate-dashboard.js   # Main generation script
├── config.json            # Configuration settings
├── package.json           # Node.js dependencies
├── dist/                  # Generated output (created by build)
│   └── index.html        # The dashboard HTML
└── .nojekyll             # Prevents Jekyll processing on GitHub Pages
```

## Troubleshooting

### Dashboard not updating
- Check that the GitHub Actions workflow completed successfully
- Verify the `gh-pages` branch exists and has recent commits
- Ensure GitHub Pages is configured to serve from the `gh-pages` branch

### Empty dashboard
- Ensure integration tests have run recently (within 30 days)
- Check that the GITHUB_TOKEN has read access to workflow runs

### API rate limits
- The script uses authenticated requests to avoid rate limits
- If you hit limits locally, wait an hour or use a different token
