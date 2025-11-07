# TrueNAS CSI Test Results Dashboard

This directory contains the test results dashboard for the TrueNAS CSI driver integration tests.

## Overview

The dashboard provides real-time visualization of:
- Test success/failure rates
- Protocol-specific results (NFS vs NVMe-oF)
- Test type breakdowns
- Recent failure tracking
- Performance metrics

## Architecture

- **Data Source**: GitHub Actions workflow runs via GitHub API
- **Generation**: Node.js script that fetches and processes test results
- **Deployment**: GitHub Pages for hosting
- **Visualization**: Chart.js for interactive charts

## Files

- `generate-dashboard.js` - Main dashboard generation script
- `package.json` - Node.js dependencies and scripts
- `dist/index.html` - Generated dashboard (created during build)

## Development

### Prerequisites

- Node.js 18+
- GitHub token with repository read access

### Local Development

```bash
# Install dependencies
npm install

# Generate dashboard locally
npm run build

# Serve locally for development
npm run dev
```

### Environment Variables

- `GITHUB_TOKEN` - GitHub personal access token for API access
- `WORKFLOW_RUN_ID` - Specific workflow run ID (optional)

## Deployment

The dashboard is automatically deployed via GitHub Actions when integration tests complete. See `.github/workflows/dashboard.yml` for the deployment workflow.

## Usage

### Viewing the Dashboard

The dashboard is available at: `https://bfenski.github.io/tns-csi/dashboard/`

### Adding to README

Include this badge in your main README:

```markdown
[![Integration Tests](https://github.com/bfenski/tns-csi/actions/workflows/integration.yml/badge.svg)](https://github.com/bfenski/tns-csi/actions/workflows/integration.yml)
[![Test Dashboard](https://img.shields.io/badge/Test%20Dashboard-View-blue)](https://bfenski.github.io/tns-csi/dashboard/)
```

## Customization

### Adding New Metrics

1. Update `parseTestResults()` in `generate-dashboard.js` to extract new data
2. Modify the HTML template to include new visualizations
3. Update Chart.js configurations for new charts

### Changing Time Range

Modify the `days` parameter in `getWorkflowRuns(days)` to change how far back to analyze:

```javascript
const runs = await getWorkflowRuns(7); // Last 7 days
```

### Custom Styling

Edit the CSS in the HTML template within `generateHTML()` function.

## Troubleshooting

### Common Issues

1. **GitHub API Rate Limits**: The script uses authenticated requests to avoid rate limits
2. **Missing Workflow Runs**: Ensure the workflow ID matches your integration test workflow
3. **Empty Dashboard**: Check that integration tests have run recently

### Debug Mode

Run with verbose logging:

```bash
DEBUG=* npm run build
```

## Contributing

1. Test changes locally with `npm run dev`
2. Ensure the dashboard builds successfully with `npm run build`
3. Test the generated HTML in multiple browsers
4. Update this README if adding new features