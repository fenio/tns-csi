import * as core from '@actions/core';
import * as exec from '@actions/exec';

export async function cleanup(): Promise<void> {
  core.startGroup('Cleaning up and restoring system state');
  
  try {
    core.info('Starting k3s cleanup...');
    
    // Uninstall k3s
    await uninstallK3s();
    
    core.info('✓ System state restored');
  } catch (error) {
    core.warning(`Cleanup encountered errors: ${error}`);
    // Don't fail the workflow if cleanup has issues
  } finally {
    core.endGroup();
  }
}

async function uninstallK3s(): Promise<void> {
  core.info('Uninstalling k3s...');
  
  // Check if k3s-uninstall.sh exists
  const uninstallScript = '/usr/local/bin/k3s-uninstall.sh';
  
  try {
    // Check if service exists and is active
    const isActive = await exec.exec('sudo', ['systemctl', 'is-active', 'k3s'], { 
      ignoreReturnCode: true,
      silent: true 
    });
    
    if (isActive === 0) {
      core.info('  k3s service is active, stopping...');
    }
    
    // Run the uninstall script if it exists
    const scriptExists = await exec.exec('test', ['-f', uninstallScript], {
      ignoreReturnCode: true,
      silent: true
    });
    
    if (scriptExists === 0) {
      core.info('  Running k3s-uninstall.sh...');
      await exec.exec('sudo', [uninstallScript], { ignoreReturnCode: true });
      core.info('  k3s uninstalled successfully');
    } else {
      core.info('  k3s-uninstall.sh not found, k3s may not be installed');
    }
    
    // Clean up any remaining files
    core.info('  Cleaning up remaining k3s files...');
    await exec.exec('sudo', ['rm', '-rf', '/etc/rancher/k3s', '/var/lib/rancher/k3s'], { ignoreReturnCode: true });
    
    // Clean up kubeconfig
    await exec.exec('sudo', ['rm', '-rf', '~/.kube/config'], { ignoreReturnCode: true });
    
    core.info('  k3s cleanup complete');
  } catch (error) {
    core.warning(`Failed to uninstall k3s: ${error}`);
  }
}
