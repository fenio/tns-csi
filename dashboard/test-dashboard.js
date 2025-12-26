#!/usr/bin/env node

// Simple test script for dashboard generation
const { getWorkflowRuns, parseTestResults, generateHTML } = require('./generate-dashboard');

async function testDashboard() {
  console.log('Testing dashboard generation...');

  try {
    // Test with mock data if no GitHub token
    if (!process.env.GITHUB_TOKEN) {
      console.log('No GITHUB_TOKEN found, using mock data for testing...');

      // Mock data matching current Ginkgo-based E2E test job naming
      const mockJobs = [
        {
          name: 'E2E: NFS',
          conclusion: 'success',
          started_at: '2024-01-01T10:00:00Z',
          completed_at: '2024-01-01T10:30:00Z',
          run_id: 123,
          html_url: 'https://github.com/fenio/tns-csi/actions/runs/123'
        },
        {
          name: 'E2E: NVMe-oF',
          conclusion: 'success',
          started_at: '2024-01-01T11:00:00Z',
          completed_at: '2024-01-01T11:45:00Z',
          run_id: 124,
          html_url: 'https://github.com/fenio/tns-csi/actions/runs/124'
        },
        {
          name: 'E2E: Shared',
          conclusion: 'failure',
          started_at: '2024-01-01T12:00:00Z',
          completed_at: '2024-01-01T12:30:00Z',
          run_id: 125,
          html_url: 'https://github.com/fenio/tns-csi/actions/runs/125'
        }
      ];

      const results = parseTestResults(mockJobs);
      const html = generateHTML(results, []);

      console.log('✓ Mock data processed successfully');
      console.log(`  - Total tests: ${results.total}`);
      console.log(`  - Passed: ${results.passed}`);
      console.log(`  - Failed: ${results.failed}`);
      console.log(`  - NFS tests: ${results.byProtocol.nfs.total}`);
      console.log(`  - NVMe-oF tests: ${results.byProtocol.nvmeof.total}`);
      console.log(`  - HTML length: ${html.length} characters`);

      return true;
    }

    // Test with real data
    console.log('Testing with real GitHub API data...');
    const runs = await getWorkflowRuns(1); // Last day

    if (runs.length === 0) {
      console.log('No recent workflow runs found');
      return true;
    }

    console.log(`Found ${runs.length} workflow runs`);

    // Test parsing
    const mockJobs = runs.slice(0, 5).map(run => ({
      name: `Test Job ${run.id}`,
      conclusion: run.conclusion,
      started_at: run.created_at,
      completed_at: run.updated_at,
      run_id: run.id,
      html_url: run.html_url
    }));

    const results = parseTestResults(mockJobs);
    const html = generateHTML(results, runs);

    console.log('✓ Real data processed successfully');
    console.log(`  - Total tests: ${results.total}`);
    console.log(`  - Passed: ${results.passed}`);
    console.log(`  - Failed: ${results.failed}`);

    return true;

  } catch (error) {
    console.error('✗ Test failed:', error.message);
    console.error('Stack trace:', error.stack);
    return false;
  }
}

if (require.main === module) {
  testDashboard().then(success => {
    process.exit(success ? 0 : 1);
  });
}