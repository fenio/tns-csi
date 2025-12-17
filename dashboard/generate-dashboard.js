#!/usr/bin/env node

const { Octokit } = require('@octokit/rest');
const fs = require('fs');
const path = require('path');
const { format, subDays, parseISO } = require('date-fns');

const octokit = new Octokit({
  auth: process.env.GITHUB_TOKEN
});

const OWNER = 'fenio';
const REPO = 'tns-csi';
const WORKFLOW_ID = 'integration.yml';

async function getWorkflowRuns(days = 30) {
  const since = subDays(new Date(), days);
  const runs = [];

  try {
    let page = 1;
    while (true) {
      const response = await octokit.actions.listWorkflowRuns({
        owner: OWNER,
        repo: REPO,
        workflow_id: WORKFLOW_ID,
        per_page: 100,
        page: page,
        created: `>=${format(since, 'yyyy-MM-dd')}`
      });

      runs.push(...response.data.workflow_runs);

      if (response.data.workflow_runs.length < 100) break;
      page++;
    }
  } catch (error) {
    console.error('Error fetching workflow runs:', error.message);
    if (error.status === 401) {
      console.error('Authentication failed. Please check your GITHUB_TOKEN.');
    } else if (error.status === 404) {
      console.error('Workflow not found. Check OWNER, REPO, and WORKFLOW_ID.');
    }
    throw error;
  }

  return runs;
}

async function getWorkflowRunDetails(runId) {
  try {
    const response = await octokit.actions.listJobsForWorkflowRun({
      owner: OWNER,
      repo: REPO,
      run_id: runId
    });

    return response.data.jobs;
  } catch (error) {
    console.error(`Error fetching jobs for run ${runId}:`, error);
    return [];
  }
}

/**
 * Determines if a job was skipped based on its status and duration.
 * @param {Object} job - The job object
 * @param {string} status - The job status
 * @returns {boolean} - True if the job was skipped
 */
function isJobSkipped(job, status) {
  if (status !== 'success' || !job.started_at || !job.completed_at) {
    return false;
  }
  const duration = (new Date(job.completed_at) - new Date(job.started_at)) / 1000;
  // If test completed in less than 30 seconds, it might have been skipped
  return duration < 30;
}

/**
 * Updates the results counters based on job status.
 * @param {Object} counters - Object with total, passed, failed, cancelled, skipped counters
 * @param {string} status - The job status
 * @param {boolean} isSkipped - Whether the job was skipped
 */
function updateStatusCounters(counters, status, isSkipped) {
  counters.total++;
  if (isSkipped) {
    counters.skipped++;
  } else if (status === 'success') {
    counters.passed++;
  } else if (status === 'failure') {
    counters.failed++;
  } else if (status === 'cancelled') {
    counters.cancelled++;
  }
}

/**
 * Parses the protocol from a job name.
 * @param {string} jobName - The job name
 * @returns {string|null} - 'nfs', 'nvmeof', or null if not found
 */
function parseProtocolFromJobName(jobName) {
  if (jobName.includes('NFS')) return 'nfs';
  if (jobName.includes('NVMe-oF')) return 'nvmeof';
  return null;
}

/**
 * Extracts the test type from a job name.
 * @param {string} jobName - The job name
 * @returns {string} - The test type (normalized to lowercase)
 */
function extractTestType(jobName) {
  return jobName
    .replace(/Integration Tests?/i, '')  // Remove "Integration Test(s)"
    .replace(/\b(NFS|NVMe-oF)\b/i, '')   // Remove protocol names
    .trim()
    .toLowerCase() || 'basic';            // Default to 'basic' if empty
}

/**
 * Tracks job duration for reporting.
 * @param {Object} results - The results object
 * @param {Object} job - The job object
 * @param {string} status - The job status
 * @param {boolean} isSkipped - Whether the job was skipped
 */
function trackJobDuration(results, job, status, isSkipped) {
  if (!job.started_at || !job.completed_at) return;
  
  const duration = (new Date(job.completed_at) - new Date(job.started_at)) / 1000 / 60; // minutes
  results.durations.push({
    name: job.name,
    duration: duration,
    status: isSkipped ? 'skipped' : status
  });
}

/**
 * Tracks recent failures for reporting.
 * @param {Object} results - The results object
 * @param {Object} job - The job object
 * @param {string} status - The job status
 */
function trackRecentFailure(results, job, status) {
  if (status !== 'failure' || results.recentFailures.length >= 10) return;
  
  results.recentFailures.push({
    name: job.name,
    runId: job.run_id,
    created: job.created_at || job.started_at,
    htmlUrl: job.html_url
  });
}

function parseTestResults(jobs) {
  const results = {
    total: 0,
    passed: 0,
    failed: 0,
    cancelled: 0,
    skipped: 0,
    byProtocol: {
      nfs: { total: 0, passed: 0, failed: 0, cancelled: 0, skipped: 0 },
      nvmeof: { total: 0, passed: 0, failed: 0, cancelled: 0, skipped: 0 }
    },
    byTestType: {},
    durations: [],
    recentFailures: []
  };

  for (const job of jobs) {
    if (!job.name.includes('Integration Tests')) continue;

    const status = job.conclusion || job.status;
    const isSkipped = isJobSkipped(job, status);

    // Update overall counters
    updateStatusCounters(results, status, isSkipped);

    // Update protocol-specific counters
    const protocol = parseProtocolFromJobName(job.name);
    if (protocol) {
      updateStatusCounters(results.byProtocol[protocol], status, isSkipped);
    }

    // Update test type counters
    const testType = extractTestType(job.name);
    if (!results.byTestType[testType]) {
      results.byTestType[testType] = { total: 0, passed: 0, failed: 0, cancelled: 0, skipped: 0 };
    }
    updateStatusCounters(results.byTestType[testType], status, isSkipped);

    // Track duration and failures
    trackJobDuration(results, job, status, isSkipped);
    trackRecentFailure(results, job, status);
  }

  return results;
}

function generateHTML(results, runs) {
  const lastUpdated = format(new Date(), 'yyyy-MM-dd HH:mm:ss');
  const totalRuns = runs.length;
  const successRate = totalRuns > 0 ? ((results.passed / results.total) * 100).toFixed(1) : 0;

  return `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>TrueNAS CSI - Test Results Dashboard</title>
    <script src="https://cdn.jsdelivr.net/npm/chart.js"></script>
    <style>
        * {
            margin: 0;
            padding: 0;
            box-sizing: border-box;
        }

        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
            background: #f5f5f5;
            color: #333;
            line-height: 1.6;
        }

        .container {
            max-width: 1200px;
            margin: 0 auto;
            padding: 20px;
        }

        .header {
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            color: white;
            padding: 30px;
            border-radius: 10px;
            margin-bottom: 30px;
            text-align: center;
        }

        .header h1 {
            font-size: 2.5em;
            margin-bottom: 10px;
        }

        .header p {
            font-size: 1.2em;
            opacity: 0.9;
        }

        .stats-grid {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(250px, 1fr));
            gap: 20px;
            margin-bottom: 30px;
        }

        .stat-card {
            background: white;
            padding: 25px;
            border-radius: 10px;
            box-shadow: 0 2px 10px rgba(0,0,0,0.1);
            text-align: center;
        }

        .stat-card h3 {
            font-size: 2em;
            margin-bottom: 5px;
            color: #333;
        }

        .stat-card p {
            color: #666;
            font-size: 0.9em;
        }

        .success-rate {
            color: #28a745;
            font-weight: bold;
        }

        .failure-rate {
            color: #dc3545;
            font-weight: bold;
        }

        .skipped-rate {
            color: #ffc107;
            font-weight: bold;
        }

        .charts-grid {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(400px, 1fr));
            gap: 20px;
            margin-bottom: 30px;
        }

        .chart-container {
            background: white;
            padding: 25px;
            border-radius: 10px;
            box-shadow: 0 2px 10px rgba(0,0,0,0.1);
        }

        .chart-container h3 {
            margin-bottom: 20px;
            text-align: center;
            color: #333;
        }

        .failures-list {
            background: white;
            padding: 25px;
            border-radius: 10px;
            box-shadow: 0 2px 10px rgba(0,0,0,0.1);
            margin-bottom: 30px;
        }

        .failures-list h3 {
            margin-bottom: 20px;
            color: #333;
        }

        .failure-item {
            padding: 15px;
            border-left: 4px solid #dc3545;
            background: #f8f9fa;
            margin-bottom: 10px;
            border-radius: 5px;
        }

        .failure-item a {
            color: #dc3545;
            text-decoration: none;
            font-weight: bold;
        }

        .failure-item a:hover {
            text-decoration: underline;
        }

        .footer {
            text-align: center;
            color: #666;
            font-size: 0.9em;
            margin-top: 30px;
        }

        @media (max-width: 768px) {
            .header h1 {
                font-size: 2em;
            }

            .stats-grid {
                grid-template-columns: 1fr;
            }

            .charts-grid {
                grid-template-columns: 1fr;
            }
        }
    </style>
</head>
<body>
    <div class="container">
        <div class="header">
            <h1>TrueNAS CSI Driver</h1>
            <p>Test Results Dashboard</p>
        </div>

        <div class="stats-grid">
            <div class="stat-card">
                <h3>${results.total}</h3>
                <p>Total Tests</p>
            </div>
            <div class="stat-card">
                <h3 class="success-rate">${results.passed}</h3>
                <p>Passed</p>
            </div>
            <div class="stat-card">
                <h3 class="failure-rate">${results.failed}</h3>
                <p>Failed</p>
            </div>
            ${results.skipped > 0 ? `
            <div class="stat-card">
                <h3 class="skipped-rate">${results.skipped}</h3>
                <p>Skipped</p>
            </div>
            ` : ''}
            <div class="stat-card">
                <h3 class="${successRate >= 95 ? 'success-rate' : 'failure-rate'}">${successRate}%</h3>
                <p>Success Rate</p>
            </div>
        </div>

        <div class="charts-grid">
            <div class="chart-container">
                <h3>Test Results by Protocol</h3>
                <canvas id="protocolChart" width="400" height="300"></canvas>
            </div>
            <div class="chart-container">
                <h3>Test Results by Type</h3>
                <canvas id="testTypeChart" width="400" height="300"></canvas>
            </div>
        </div>

        ${results.recentFailures.length > 0 ? `
        <div class="failures-list">
            <h3>Recent Failures</h3>
            ${results.recentFailures.map(failure => `
                <div class="failure-item">
                    <strong>${failure.name}</strong><br>
                    <small>${format(parseISO(failure.created), 'yyyy-MM-dd HH:mm')}</small><br>
                    <a href="${failure.htmlUrl}" target="_blank">View Details →</a>
                </div>
            `).join('')}
        </div>
        ` : ''}

        <div class="footer">
            <p>Last updated: ${lastUpdated} | Total workflow runs analyzed: ${totalRuns}</p>
        </div>
    </div>

    <script>
        // Protocol Chart
        const protocolCtx = document.getElementById('protocolChart').getContext('2d');
        new Chart(protocolCtx, {
            type: 'bar',
            data: {
                labels: ['NFS', 'NVMe-oF'],
                datasets: [{
                    label: 'Passed',
                    data: [${results.byProtocol.nfs.passed}, ${results.byProtocol.nvmeof.passed}],
                    backgroundColor: '#28a745'
                }, {
                    label: 'Failed',
                    data: [${results.byProtocol.nfs.failed}, ${results.byProtocol.nvmeof.failed}],
                    backgroundColor: '#dc3545'
                }${results.skipped > 0 ? `, {
                    label: 'Skipped',
                    data: [${results.byProtocol.nfs.skipped}, ${results.byProtocol.nvmeof.skipped}],
                    backgroundColor: '#ffc107'
                }` : ''}]
            },
            options: {
                responsive: true,
                scales: {
                    x: { stacked: true },
                    y: { stacked: true }
                }
            }
        });

        // Test Type Chart
        const testTypeCtx = document.getElementById('testTypeChart').getContext('2d');
        const testTypeData = ${JSON.stringify(results.byTestType)};
        const testTypes = Object.keys(testTypeData);
        
        new Chart(testTypeCtx, {
            type: 'bar',
            data: {
                labels: testTypes,
                datasets: [{
                    label: 'Passed',
                    data: testTypes.map(type => testTypeData[type].passed),
                    backgroundColor: '#28a745'
                }, {
                    label: 'Failed',
                    data: testTypes.map(type => testTypeData[type].failed),
                    backgroundColor: '#dc3545'
                }${results.skipped > 0 ? `, {
                    label: 'Skipped',
                    data: testTypes.map(type => testTypeData[type].skipped || 0),
                    backgroundColor: '#ffc107'
                }` : ''}]
            },
            options: {
                responsive: true,
                scales: {
                    x: { stacked: true },
                    y: { stacked: true }
                }
            }
        });
    </script>
</body>
</html>`;
}

async function main() {
  console.log('Fetching workflow runs...');
  
  if (!process.env.GITHUB_TOKEN) {
    console.error('ERROR: GITHUB_TOKEN environment variable is required');
    process.exit(1);
  }
  
  const runs = await getWorkflowRuns(30);

  if (runs.length === 0) {
    console.warn('WARNING: No workflow runs found in the last 30 days');
    console.log('Generating empty dashboard...');
  } else {
    console.log(`Found ${runs.length} workflow runs`);
  }

  const allJobs = [];
  for (const run of runs.slice(0, 10)) { // Limit to recent runs for performance
    console.log(`Fetching jobs for run ${run.id}...`);
    const jobs = await getWorkflowRunDetails(run.id);
    allJobs.push(...jobs);
  }

  console.log(`Processing ${allJobs.length} jobs...`);
  const results = parseTestResults(allJobs);

  console.log('Generating HTML...');
  const html = generateHTML(results, runs);

  // Ensure dist directory exists
  const distDir = path.join(__dirname, 'dist');
  if (!fs.existsSync(distDir)) {
    fs.mkdirSync(distDir);
  }

  // Write HTML file
  fs.writeFileSync(path.join(distDir, 'index.html'), html);

  console.log('Dashboard generated successfully!');
  console.log(`Results: ${results.passed} passed, ${results.failed} failed out of ${results.total} total tests`);
}

if (require.main === module) {
  main().catch(console.error);
}

module.exports = { getWorkflowRuns, getWorkflowRunDetails, parseTestResults, generateHTML };