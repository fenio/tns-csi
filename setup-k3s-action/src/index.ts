import * as core from '@actions/core';
import { main } from './main';
import { cleanup } from './cleanup';

// Main entry point
if (!core.getState('isPost')) {
  // This is the main run
  main().catch(error => {
    core.setFailed(error.message);
    process.exit(1);
  });
} else {
  // This is the post run (cleanup)
  cleanup().catch(error => {
    core.warning(`Cleanup failed: ${error.message}`);
    // Don't fail the workflow if cleanup fails
  });
}
