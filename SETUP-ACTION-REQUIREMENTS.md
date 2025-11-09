# Setup Action Requirements

## Context

This repository tests 4 different Kubernetes distributions using separate GitHub Actions:
- setup-kubesolo
- setup-k3s
- setup-k0s
- setup-minikube

## Critical Requirement: Each Action Must Be Self-Contained

**IMPORTANT:** Each setup action MUST work independently on a pristine Ubuntu installation. Even though the workflow currently runs tests sequentially with `needs:` dependencies, each action must:

1. **Install kubectl** - Every action must ensure kubectl is available, either by:
   - Installing kubectl as part of the action
   - Including kubectl installation instructions in the action's documentation
   - The action should NOT assume kubectl is already present

2. **Not rely on previous tests** - Each action must:
   - Work on a completely clean system
   - Install all its own dependencies
   - Clean up after itself completely
   - Not leave any state that affects subsequent tests

3. **Handle pristine Ubuntu 25.10** - The actions run on:
   - Fresh Ubuntu 25.10 installation
   - Self-hosted GitHub runner with user `tns`
   - sudo-rs (Rust sudo implementation) instead of traditional sudo
   - Passwordless sudo configured for the runner user

## Instructions for Each Setup Action Repository

When working on any of these action repositories (setup-kubesolo, setup-k3s, setup-k0s, setup-minikube), add this to their AGENTS.md:

---

# Agent Instructions for [Distribution] Setup Action

## Critical Requirements

This action MUST work independently on a pristine Ubuntu 25.10 installation.

### kubectl Installation

**IMPORTANT:** This action MUST ensure kubectl is available. Do NOT assume kubectl is pre-installed.

Options:
1. Install kubectl as part of this action's setup process
2. Document clearly that kubectl must be installed before using this action
3. Check if kubectl exists and install it if missing

### Pristine Environment Assumptions

This action will be tested on:
- **Ubuntu 25.10** (fresh installation)
- **sudo-rs** (Rust implementation of sudo, not traditional sudo)
- **Self-hosted GitHub runner** with user having passwordless sudo
- **No pre-installed Kubernetes tools** (no kubectl, no container runtimes unless part of the distribution)

### Cleanup Requirements

This action MUST fully clean up after itself:
- Stop all services started by the distribution
- Remove binaries installed
- Clean up configuration files
- Restore any system state that was modified
- Ensure the system is ready for the next distribution test

### Testing

The action is tested in a workflow where multiple distributions are tested sequentially, but each test must work as if it's the ONLY test running on a pristine system.

---

## Current State

As of this writing:
- ✅ setup-kubesolo: kubeconfig permissions fixed, needs kubectl installation
- ❓ setup-k3s: Unknown if self-contained (k3s includes kubectl)
- ❓ setup-k0s: Unknown if self-contained
- ❓ setup-minikube: Unknown if self-contained
