---
permalink: /tns-csi/dashboard/
---

# TrueNAS CSI Test Results Dashboard

This dashboard provides real-time insights into the integration test results for the TrueNAS CSI driver.

## Quick Stats

- **Total Tests Run**: {{ site.data.dashboard.total_tests }}
- **Success Rate**: {{ site.data.dashboard.success_rate }}%
- **Last Updated**: {{ site.data.dashboard.last_updated }}

## Test Results by Protocol

| Protocol | Total | Passed | Failed | Success Rate |
|----------|-------|--------|--------|--------------|
| NFS | {{ site.data.dashboard.nfs.total }} | {{ site.data.dashboard.nfs.passed }} | {{ site.data.dashboard.nfs.failed }} | {{ site.data.dashboard.nfs.success_rate }}% |
| NVMe-oF | {{ site.data.dashboard.nvmeof.total }} | {{ site.data.dashboard.nvmeof.passed }} | {{ site.data.dashboard.nvmeof.failed }} | {{ site.data.dashboard.nvmeof.success_rate }}% |

## Recent Test Runs

{% for run in site.data.dashboard.recent_runs %}
### {{ run.name }} - {{ run.status }}
- **Started**: {{ run.created_at }}
- **Duration**: {{ run.duration }} minutes
- **URL**: [View Details]({{ run.html_url }})
{% endfor %}

---
*Dashboard automatically updated after each integration test run.*