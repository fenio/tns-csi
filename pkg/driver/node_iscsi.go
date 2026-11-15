package driver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/fenio/tns-csi/pkg/cmdexec"
	"github.com/fenio/tns-csi/pkg/mount"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
)

// Static errors for iSCSI operations.
var (
	ErrISCSIAdmNotFound      = errors.New("iscsiadm command not found - please install open-iscsi")
	ErrISCSIDeviceNotFound   = errors.New("iSCSI device not found")
	ErrISCSIDeviceTimeout    = errors.New("timeout waiting for iSCSI device to appear")
	ErrISCSILoginFailed      = errors.New("failed to login to iSCSI target")
	ErrISCSIDiscoveryFailed  = errors.New("iSCSI discovery failed - iscsid may not be running or accessible")
	ErrISCSITargetNotInDB    = errors.New("iSCSI target not found in node database after discovery")
	ErrISCSIEmptyIQN         = errors.New("empty iSCSI IQN")
	ErrISCSILogoutTimeout    = errors.New("timeout waiting for iSCSI session logout")
	ErrISCSINonDevicePath    = errors.New("iSCSI staging path resolved to a non-device path")
	ErrISCSIMalformedSession = errors.New("malformed iSCSI session output")
	ErrISCSINodeDBLocked     = errors.New("open-iscsi node database lock is held (lock.write); an iscsiadm may have been killed while holding it")
	ErrISCSIAdmStillRunning  = errors.New("iscsiadm still running")
	errISCSISessionQuery     = errors.New("iSCSI session query failed")
)

// defaultISCSIMountOptions are sensible defaults for iSCSI filesystem mounts.
var defaultISCSIMountOptions = []string{zfsNoatime, "_netdev"}

const (
	iscsiStagingMetadataSuffix = ".tns-csi-iscsi"
	iscsiTargetPrefix          = "Target:"
)

// iscsiConnectionParams holds validated iSCSI connection parameters.
type iscsiConnectionParams struct {
	iqn       string
	server    string
	port      string
	serverIPs []netip.Addr
	lun       int
}

// stageISCSIVolume stages an iSCSI volume by logging into the target.
// It uses a retry mechanism to handle transient device stability issues.
func (s *NodeService) stageISCSIVolume(ctx context.Context, req *csi.NodeStageVolumeRequest, volumeContext map[string]string) (*csi.NodeStageVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	stagingTargetPath := req.GetStagingTargetPath()
	volumeCapability := req.GetVolumeCapability()

	// Validate and extract connection parameters
	params, err := s.validateISCSIParams(volumeContext)
	if err != nil {
		return nil, err
	}
	params.serverIPs, err = resolveISCSIServerIPs(ctx, params.server)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "cannot resolve iSCSI server %s: %v", params.server, err)
	}
	if err := writeISCSIStagingIQN(stagingTargetPath, params.iqn); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to persist iSCSI staging metadata: %v", err)
	}

	isBlockVolume := volumeCapability.GetBlock() != nil
	datasetName := volumeContext["datasetName"]
	klog.V(4).Infof("Staging iSCSI volume %s (block mode: %v): server=%s:%s, IQN=%s, LUN=%d, dataset=%s",
		volumeID, isBlockVolume, params.server, params.port, params.iqn, params.lun, datasetName)

	// Try to reuse existing connection (idempotency)
	if devicePath, findErr := s.findISCSIDevice(ctx, params); findErr == nil && devicePath != "" {
		klog.V(4).Infof("iSCSI device already connected at %s - reusing existing connection", devicePath)
		return s.stageISCSIDevice(ctx, volumeID, devicePath, stagingTargetPath, volumeCapability, isBlockVolume, volumeContext)
	} else if errors.Is(findErr, errISCSISessionQuery) {
		// As before this change: a failed session query does not block a fresh login, which
		// reports "already present" if a session exists after all.
		klog.Warningf("Cannot check for an existing iSCSI session for %s (%v); continuing with login", params.iqn, findErr)
	} else if findErr != nil && !errors.Is(findErr, ErrISCSIDeviceNotFound) {
		return nil, status.Errorf(codes.FailedPrecondition, "cannot safely select an iSCSI device for %s: %v", params.iqn, findErr)
	}

	// Check if iscsiadm is installed
	if checkErr := s.checkISCSIAdm(ctx); checkErr != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "open-iscsi not available: %v", checkErr)
	}

	// Retry parameters for handling iSCSI service availability issues.
	// The iSCSI service on TrueNAS may be temporarily unavailable during
	// service reloads triggered by target creation.
	const (
		maxRetries = 5
		retryDelay = 15 * time.Second
	)

	var lastErr error
	for attempt := 1; attempt <= maxRetries && ctx.Err() == nil; attempt++ {
		if attempt > 1 {
			klog.Infof("iSCSI staging attempt %d/%d for volume %s", attempt, maxRetries, volumeID)
		}

		// Discover and login to iSCSI target
		if loginErr := s.loginISCSITarget(ctx, params); loginErr != nil {
			lastErr = loginErr
			klog.Warningf("iSCSI login attempt %d failed: %v", attempt, loginErr)
			if attempt < maxRetries {
				sleepContext(ctx, retryDelay)
			}
			continue
		}

		// Wait for device to appear
		devicePath, err := s.waitForISCSIDevice(ctx, params, 30*time.Second)
		if err != nil {
			lastErr = err
			klog.Warningf("iSCSI device wait failed on attempt %d: %v", attempt, err)
			// Cleanup: logout before retry
			if logoutErr := s.logoutISCSITarget(ctx, params); logoutErr != nil {
				klog.Warningf("Failed to logout from iSCSI target after device wait failure: %v", logoutErr)
			}
			if attempt < maxRetries {
				sleepContext(ctx, retryDelay)
			}
			continue
		}

		klog.V(4).Infof("iSCSI device connected at %s (IQN: %s, LUN: %d, dataset: %s) on attempt %d",
			devicePath, params.iqn, params.lun, datasetName, attempt)

		// Try staging - if device becomes unavailable during staging, retry the whole connection
		stageResp, stageErr := s.stageISCSIDevice(ctx, volumeID, devicePath, stagingTargetPath, volumeCapability, isBlockVolume, volumeContext)
		if stageErr == nil {
			return stageResp, nil
		}

		// Check if this is a retryable error (device disappeared during staging)
		if status.Code(stageErr) == codes.Unavailable {
			lastErr = stageErr
			klog.Warningf("iSCSI staging failed on attempt %d (device unstable): %v", attempt, stageErr)
			// Logout and retry - the device may have become stale
			if logoutErr := s.logoutISCSITarget(ctx, params); logoutErr != nil {
				klog.Warningf("Failed to logout from iSCSI target after staging failure: %v", logoutErr)
			}
			if attempt < maxRetries {
				sleepContext(ctx, retryDelay)
			}
			continue
		}

		// Non-retryable error - fail immediately
		return nil, stageErr
	}

	if ctxErr := ctx.Err(); ctxErr != nil {
		// Report what actually went wrong, not only that time ran out.
		return nil, status.Errorf(status.FromContextError(ctxErr).Code(), "iSCSI staging of volume %s stopped (%v); last error: %v", volumeID, ctxErr, lastErr)
	}
	// All retries exhausted
	return nil, status.Errorf(codes.Internal, "Failed to stage iSCSI volume after %d attempts: %v", maxRetries, lastErr)
}

// sleepContext waits for d or until ctx ends, and reports whether the full wait elapsed.
func sleepContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// validateISCSIParams validates and extracts iSCSI connection parameters from volume context.
func (s *NodeService) validateISCSIParams(volumeContext map[string]string) (*iscsiConnectionParams, error) {
	params := &iscsiConnectionParams{
		iqn:    volumeContext[VolumeContextKeyISCSIIQN],
		server: volumeContext["server"],
		port:   volumeContext["port"],
		lun:    0, // Always LUN 0 with dedicated targets
	}

	// Log all volume context keys for debugging
	klog.Infof("iSCSI validateISCSIParams - volume context keys: %v", volumeContext)
	klog.Infof("iSCSI validateISCSIParams - extracted IQN: '%s', server: '%s', port: '%s'",
		params.iqn, params.server, params.port)

	if params.iqn == "" || params.server == "" {
		return nil, status.Error(codes.InvalidArgument, "iSCSI IQN and server must be provided in volume context")
	}

	// Default port
	if params.port == "" {
		params.port = "3260"
	}

	return params, nil
}

// checkISCSIAdm checks if iscsiadm is available (either directly or via nsenter).
func (s *NodeService) checkISCSIAdm(ctx context.Context) error {
	if _, err := s.runISCSIAdm(ctx, 5*time.Second, "--version"); err != nil {
		if iscsiadmErrorIs(err) || ctx.Err() != nil {
			return fmt.Errorf("checking iscsiadm: %w", err)
		}
		return fmt.Errorf("%w: %w", ErrISCSIAdmNotFound, err)
	}
	return nil
}

// iscsiErrNoObjsFound is iscsiadm's exit code for "No records found" (ISCSI_ERR_NO_OBJS_FOUND).
const iscsiErrNoObjsFound = 21

// loginISCSITarget logs into an iSCSI target, discovering it first only when this node has
// no record for it yet.
func (s *NodeService) loginISCSITarget(ctx context.Context, params *iscsiConnectionParams) error {
	portal := net.JoinHostPort(strings.Trim(params.server, "[]"), params.port)
	if len(params.serverIPs) == 0 {
		ips, err := resolveISCSIServerIPs(ctx, params.server)
		if err != nil {
			return err
		}
		params.serverIPs = ips
	}

	// A SendTargets discovery rewrites the record of every target the portal advertises
	// (hundreds with one target per volume) under open-iscsi's DB lock. An existing record
	// for this target is enough to log in, so skip discovery when there is one (#336).
	selectedPortal, err := s.selectISCSINodeRecord(ctx, params)
	discovered := false
	if err != nil {
		if !errors.Is(err, errISCSIPortalNotFound) {
			return err
		}
		selectedPortal, err = s.discoverISCSINodeRecord(ctx, params, portal)
		if err != nil {
			return err
		}
		discovered = true
	} else {
		klog.Infof("iSCSI: node record for %s already present at %s; skipping discovery", params.iqn, selectedPortal)
	}

	loginErr := s.runISCSILogin(ctx, params, selectedPortal)
	if loginErr == nil || discovered || iscsiadmErrorIs(loginErr) || ctx.Err() != nil {
		return loginErr
	}
	// The record may be stale (target re-created, portal changed): refresh it and retry once.
	klog.Warningf("iSCSI login to %s using the existing node record failed (%v); refreshing it with discovery", params.iqn, loginErr)
	selectedPortal, err = s.discoverISCSINodeRecord(ctx, params, portal)
	if err != nil {
		return errors.Join(loginErr, err)
	}
	if retryErr := s.runISCSILogin(ctx, params, selectedPortal); retryErr != nil {
		return errors.Join(fmt.Errorf("login after refreshing the node record: %w", retryErr),
			fmt.Errorf("login with the existing node record: %w", loginErr))
	}
	return nil
}

// selectISCSINodeRecord lists this node's records and selects the one for the target on the
// configured server. It returns errISCSIPortalNotFound when there is none.
// Do not pass -T here: open-iscsi prints configuration blocks for a filtered node query,
// even with -P 0. List compact records and filter the IQN in Go.
func (s *NodeService) selectISCSINodeRecord(ctx context.Context, params *iscsiConnectionParams) (string, error) {
	output, err := s.runISCSIAdm(ctx, iscsiadmDBTimeout, "-m", "node", "-P", "0")
	if err != nil {
		if code, ok := cmdexec.ExitCode(err); !ok || code != iscsiErrNoObjsFound {
			klog.Errorf("iSCSI: listing node records failed: %v, output: %s", err, string(output))
			return "", fmt.Errorf("failed to list iSCSI node records: %w, output: %s", err, strings.TrimSpace(string(output)))
		}
		output = nil
	}
	// TrueNAS may return multiple interfaces for one IQN. Select the record on the configured
	// server (resolving hostnames to the advertised IP), not all records.
	return selectISCSINodePortal(string(output), params.iqn, params.server, params.port, params.serverIPs)
}

// discoverISCSINodeRecord runs SendTargets discovery against portal, then selects the
// target's record.
func (s *NodeService) discoverISCSINodeRecord(ctx context.Context, params *iscsiConnectionParams, portal string) (string, error) {
	klog.Infof("iSCSI: Discovering targets at portal %s for IQN %s", portal, params.iqn)
	output, discoveryErr := s.runISCSIAdm(ctx, iscsiadmDBTimeout, "-m", "discovery", "-t", "sendtargets", "-p", portal)
	if discoveryErr != nil {
		klog.Errorf("iSCSI discovery failed at %s: %v, output: %s", portal, discoveryErr, string(output))
		if iscsiadmErrorIs(discoveryErr) || ctx.Err() != nil {
			return "", fmt.Errorf("iSCSI discovery at %s: %w", portal, discoveryErr)
		}
		if strings.Contains(string(output), "connect") || strings.Contains(string(output), "Connection refused") {
			return "", fmt.Errorf("%w: %s", ErrISCSIDiscoveryFailed, string(output))
		}
		// Continue: the target may already be known from a previous discovery.
		klog.Warningf("Continuing despite discovery failure - target may already be known")
	} else {
		klog.Infof("iSCSI discovery successful at %s, discovered targets:\n%s", portal, string(output))
	}

	selected, err := s.selectISCSINodeRecord(ctx, params)
	switch {
	case err == nil:
		return selected, nil
	case discoveryErr != nil:
		return "", fmt.Errorf("%w (discovery at %s failed: %w, output: %s)", err, portal, discoveryErr, strings.TrimSpace(string(output)))
	case errors.Is(err, errISCSIPortalNotFound):
		// Discovery succeeded and still did not return the target: now TrueNAS is the suspect.
		return "", fmt.Errorf("%w - check that the TrueNAS iSCSI service is running and the target is configured: %w", ErrISCSITargetNotInDB, err)
	default:
		return "", err
	}
}

func (s *NodeService) runISCSILogin(ctx context.Context, params *iscsiConnectionParams, selectedPortal string) error {
	klog.Infof("Logging into iSCSI target %s through %s", params.iqn, selectedPortal)
	output, err := s.runISCSIAdm(ctx, iscsiadmDBTimeout, "-m", "node", "-T", params.iqn, "-p", selectedPortal, "--login")
	if err != nil {
		// Check if already logged in
		alreadyLoggedIn := strings.Contains(string(output), "already present") ||
			strings.Contains(string(output), "session already exists")
		if alreadyLoggedIn {
			klog.V(4).Infof("iSCSI target already logged in: %s", params.iqn)
			return nil
		}
		klog.Errorf("iSCSI login failed for target %s: %v, output: %s", params.iqn, err, string(output))
		if iscsiadmErrorIs(err) {
			return fmt.Errorf("%w: %w", ErrISCSILoginFailed, err)
		}
		return fmt.Errorf("%w: %w, output: %s", ErrISCSILoginFailed, err, string(output))
	}

	klog.Infof("Successfully logged into iSCSI target: %s, output: %s", params.iqn, string(output))
	return nil
}

// logoutISCSITarget logs out from an iSCSI target.
func (s *NodeService) logoutISCSITarget(ctx context.Context, params *iscsiConnectionParams) error {
	if s.logoutISCSITargetFn != nil {
		return s.logoutISCSITargetFn(ctx, params)
	}

	klog.V(4).Infof("Logging out from iSCSI target: %s", params.iqn)
	operationCtx, operationCancel := context.WithTimeout(ctx, 30*time.Second)
	defer operationCancel()

	var logoutErr error
	for {
		if operationErr := operationCtx.Err(); operationErr != nil {
			return iscsiLogoutContextError(params.iqn, operationErr, logoutErr)
		}

		sessionIDs, err := s.findISCSISessionIDs(operationCtx, params.iqn)
		if err != nil {
			if operationErr := operationCtx.Err(); operationErr != nil {
				return iscsiLogoutContextError(params.iqn, operationErr, logoutErr)
			}
			return errors.Join(logoutErr, fmt.Errorf("failed to find active iSCSI sessions for %s: %w", params.iqn, err))
		}
		if len(sessionIDs) == 0 {
			klog.V(4).Infof("Successfully logged out from iSCSI target: %s", params.iqn)
			return nil
		}

		logoutErr = errors.Join(logoutErr, s.logoutISCSISessions(operationCtx, params.iqn, sessionIDs))

		select {
		case <-time.After(250 * time.Millisecond):
		case <-operationCtx.Done():
			return iscsiLogoutContextError(params.iqn, operationCtx.Err(), logoutErr)
		}
	}
}

func (s *NodeService) logoutISCSISessions(ctx context.Context, iqn string, sessionIDs []string) error {
	errorsBySession := make(chan error, len(sessionIDs))
	var waitGroup sync.WaitGroup
	for _, sessionID := range sessionIDs {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			output, err := s.runISCSIAdm(ctx, 15*time.Second, "-m", "session", "-r", sessionID, "--logout")
			if err != nil {
				klog.V(4).Infof("iSCSI logout command returned an error for %s session %s: %v, output: %s", iqn, sessionID, err, string(output))
				errorsBySession <- fmt.Errorf("session %s: %w, output: %s", sessionID, err, string(output))
			}
		}()
	}
	waitGroup.Wait()
	close(errorsBySession)

	var logoutErr error
	for err := range errorsBySession {
		logoutErr = errors.Join(logoutErr, err)
	}
	return logoutErr
}

func iscsiLogoutContextError(iqn string, operationErr, logoutErr error) error {
	if errors.Is(operationErr, context.Canceled) {
		return errors.Join(logoutErr, fmt.Errorf("iSCSI logout canceled for %s: %w", iqn, operationErr))
	}
	timeoutErr := fmt.Errorf("%w for IQN %s: %w", ErrISCSILogoutTimeout, iqn, operationErr)
	if logoutErr == nil {
		return timeoutErr
	}
	return fmt.Errorf("iSCSI logout command failed for %s: %w; session verification failed: %w", iqn, logoutErr, timeoutErr)
}

func (s *NodeService) findISCSISessionIDs(ctx context.Context, iqn string) ([]string, error) {
	output, err := s.runISCSIAdm(ctx, 5*time.Second, "-m", "session")
	if err != nil {
		lowerOutput := strings.ToLower(string(output))
		if strings.Contains(lowerOutput, "no active sessions") ||
			strings.Contains(lowerOutput, "no matching sessions") ||
			strings.Contains(lowerOutput, "no records found") {

			return nil, nil
		}
		return nil, fmt.Errorf("iscsiadm session query failed: %w, output: %s", err, string(output))
	}

	return parseISCSISessionIDs(string(output), iqn)
}

func parseISCSISessionIDs(output, targetIQN string) ([]string, error) {
	var sessionIDs []string
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if !containsExactField(fields, targetIQN) {
			continue
		}
		if len(fields) < 2 || !strings.HasPrefix(fields[1], "[") || !strings.HasSuffix(fields[1], "]") {
			return nil, fmt.Errorf("%w for target %s: %s", ErrISCSIMalformedSession, targetIQN, line)
		}

		sessionID := strings.TrimSuffix(strings.TrimPrefix(fields[1], "["), "]")
		if _, err := strconv.ParseUint(sessionID, 10, 64); err != nil {
			return nil, fmt.Errorf("invalid iSCSI session ID %q for target %s: %w", sessionID, targetIQN, err)
		}
		sessionIDs = append(sessionIDs, sessionID)
	}
	return sessionIDs, nil
}

func containsExactField(fields []string, value string) bool {
	for _, field := range fields {
		if field == value {
			return true
		}
	}
	return false
}

// findISCSIDevice finds the device path for an iSCSI LUN.
func (s *NodeService) findISCSIDevice(ctx context.Context, params *iscsiConnectionParams) (string, error) {
	// Query active sessions with detail level 3 to see attached devices
	output, err := s.runISCSIAdm(ctx, iscsiadmQueryTimeout, "-m", "session", "-P", "3")

	// Always log the output for debugging
	klog.Infof("iscsiadm -m session -P 3: err=%v, output:\n%s", err, string(output))

	if err != nil {
		if code, ok := cmdexec.ExitCode(err); ok && code == iscsiErrNoObjsFound {
			return "", ErrISCSIDeviceNotFound // no active sessions
		}
		return "", fmt.Errorf("%w: %w, output: %s", errISCSISessionQuery, err, strings.TrimSpace(string(output)))
	}

	addresses := params.serverIPs
	if len(addresses) == 0 {
		addresses, err = resolveISCSIServerIPs(ctx, params.server)
		if err != nil {
			return "", err
		}
	}
	deviceName, parseErr := parseISCSISessionDeviceForPortal(string(output), params.iqn, params.server, params.port, addresses)
	if parseErr != nil {
		return "", parseErr
	}
	if deviceName == "" {
		klog.Infof("parseISCSISessionDevice found no device for IQN: %s", params.iqn)
		return "", ErrISCSIDeviceNotFound
	}

	devicePath := "/dev/" + deviceName
	klog.Infof("Found iSCSI device: %s", devicePath)
	return devicePath, nil
}

// waitForISCSIDevice waits for the iSCSI device to appear after login.
func (s *NodeService) waitForISCSIDevice(ctx context.Context, params *iscsiConnectionParams, timeout time.Duration) (string, error) {
	klog.Infof("Waiting for iSCSI device for IQN %s (timeout: %v)", params.iqn, timeout)

	deadline := time.Now().Add(timeout)
	var lastQueryErr error
	for attempt := 1; time.Now().Before(deadline); attempt++ {
		devicePath, err := s.findISCSIDevice(ctx, params)
		if errors.Is(err, errISCSISessionQuery) {
			// A failed or slow session query says nothing about the device; keep polling.
			lastQueryErr = err
			klog.Warningf("iSCSI session query failed while waiting for %s: %v", params.iqn, err)
		} else if err != nil && !errors.Is(err, ErrISCSIDeviceNotFound) {
			return "", err
		}
		if err == nil && devicePath != "" {
			if _, statErr := os.Stat(devicePath); statErr == nil {
				klog.Infof("iSCSI device ready: %s (attempt %d)", devicePath, attempt)
				return devicePath, nil
			}
		}
		if !sleepContext(ctx, 2*time.Second) {
			return "", ctx.Err()
		}
	}

	if lastQueryErr != nil {
		return "", fmt.Errorf("%w (last session query error: %w)", ErrISCSIDeviceTimeout, lastQueryErr)
	}
	return "", ErrISCSIDeviceTimeout
}

// stageISCSIDevice stages an iSCSI device as either block or filesystem volume.
func (s *NodeService) stageISCSIDevice(ctx context.Context, volumeID, devicePath, stagingTargetPath string, volumeCapability *csi.VolumeCapability, isBlockVolume bool, volumeContext map[string]string) (*csi.NodeStageVolumeResponse, error) {
	// Verify device still exists before proceeding (it may have disappeared due to race conditions
	// with previous volume cleanup or iSCSI session issues)
	if _, err := os.Stat(devicePath); err != nil {
		klog.Warningf("iSCSI device %s disappeared before staging could complete: %v", devicePath, err)
		return nil, status.Errorf(codes.Unavailable,
			"iSCSI device %s became unavailable: %v", devicePath, err)
	}

	// For filesystem volumes, wait for device to be fully initialized
	if !isBlockVolume {
		if err := waitForDeviceInitialization(ctx, devicePath); err != nil {
			// Check if device disappeared during initialization
			if _, statErr := os.Stat(devicePath); statErr != nil {
				return nil, status.Errorf(codes.Unavailable,
					"iSCSI device %s became unavailable during initialization: %v", devicePath, err)
			}
			return nil, status.Errorf(codes.Internal, "Device initialization timeout: %v", err)
		}

		// Force device rescan
		if err := forceDeviceRescan(ctx, devicePath); err != nil {
			klog.Warningf("Device rescan warning for %s: %v (continuing anyway)", devicePath, err)
		}

		// Stabilization delay
		const deviceMetadataDelay = 2 * time.Second
		klog.V(4).Infof("Waiting %v for device %s metadata to stabilize", deviceMetadataDelay, devicePath)
		time.Sleep(deviceMetadataDelay)

		// Verify device still exists after stabilization
		if _, err := os.Stat(devicePath); err != nil {
			klog.Warningf("iSCSI device %s disappeared after stabilization: %v", devicePath, err)
			return nil, status.Errorf(codes.Unavailable,
				"iSCSI device %s became unavailable after stabilization: %v", devicePath, err)
		}
	}

	if isBlockVolume {
		return s.stageBlockDevice(devicePath, stagingTargetPath)
	}
	return s.formatAndMountISCSIDevice(ctx, volumeID, devicePath, stagingTargetPath, volumeCapability, volumeContext)
}

// formatAndMountISCSIDevice formats (if needed) and mounts an iSCSI device.
func (s *NodeService) formatAndMountISCSIDevice(ctx context.Context, volumeID, devicePath, stagingTargetPath string, volumeCapability *csi.VolumeCapability, volumeContext map[string]string) (*csi.NodeStageVolumeResponse, error) {
	datasetName := volumeContext["datasetName"]
	iqn := volumeContext[VolumeContextKeyISCSIIQN]
	klog.V(4).Infof("Formatting and mounting iSCSI device: device=%s, path=%s, volume=%s, dataset=%s, IQN=%s",
		devicePath, stagingTargetPath, volumeID, datasetName, iqn)

	mounted, mountErr := ensureStagingTarget(ctx, stagingTargetPath)
	if mountErr != nil {
		return nil, mountErr
	}
	if mounted {
		klog.V(4).Infof("Staging path %s is already mounted", stagingTargetPath)
		return &csi.NodeStageVolumeResponse{}, nil
	}

	// Log device information
	s.logDeviceInfo(ctx, devicePath)

	// Verify device size
	if err := s.verifyDeviceSize(ctx, devicePath, volumeContext); err != nil {
		klog.Errorf("Device size verification FAILED for %s: %v", devicePath, err)
		return nil, status.Errorf(codes.FailedPrecondition,
			"Device size mismatch detected - refusing to mount: %v", err)
	}

	// Determine filesystem type
	fsType := "ext4"
	if mnt := volumeCapability.GetMount(); mnt != nil && mnt.FsType != "" {
		fsType = mnt.FsType
	}

	// Check if device is cloned from snapshot
	isClone := false
	if cloned, exists := volumeContext[VolumeContextKeyClonedFromSnap]; exists && cloned == VolumeContextValueTrue {
		isClone = true
		klog.V(4).Infof("Volume %s was cloned from snapshot - adding stabilization delay", volumeID)
		const cloneStabilizationDelay = 5 * time.Second
		time.Sleep(cloneStabilizationDelay)
	}

	// Handle formatting
	mkfsArgs, mkfsErr := mkfsOptionArgs(volumeContext[VolumeContextKeyMkfsOptions], fsType)
	if mkfsErr != nil {
		return nil, mkfsErr
	}
	newlyFormatted, formatErr := s.handleDeviceFormatting(ctx, volumeID, devicePath, filesystemSpec{fsType: fsType, mkfsArgs: mkfsArgs}, datasetName, iqn, isClone)
	if formatErr != nil {
		return nil, formatErr
	}
	if checkErr := s.checkFilesystemBeforeMount(ctx, devicePath, volumeContext[VolumeContextKeyFilesystemCheck], newlyFormatted); checkErr != nil {
		return nil, checkErr
	}

	// Mount the device
	klog.V(4).Infof("Mounting device %s to %s", devicePath, stagingTargetPath)

	var userMountOptions []string
	if mnt := volumeCapability.GetMount(); mnt != nil {
		userMountOptions = mnt.MountFlags
	}
	mountOptions := getISCSIMountOptions(userMountOptions)

	klog.V(4).Infof("iSCSI mount options: user=%v, final=%v", userMountOptions, mountOptions)

	args := []string{devicePath, stagingTargetPath}
	if len(mountOptions) > 0 {
		args = []string{"-o", mount.JoinMountOptions(mountOptions), devicePath, stagingTargetPath}
	}

	output, err := runMount(ctx, blockStageMountTimeout, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to mount device: %v, output: %s", err, string(output))
	}

	klog.V(4).Infof("Mounted iSCSI device to staging path")
	return &csi.NodeStageVolumeResponse{}, nil
}

// unstageISCSIVolume unstages an iSCSI volume by logging out from the target.
func (s *NodeService) unstageISCSIVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest, volumeContext map[string]string) (*csi.NodeUnstageVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	stagingTargetPath := req.GetStagingTargetPath()

	klog.V(4).Infof("Unstaging iSCSI volume %s from %s", volumeID, stagingTargetPath)

	iqn := s.resolveISCSIUnstageIQN(ctx, stagingTargetPath, volumeContext)
	if iqn == "" {
		if _, statErr := os.Lstat(stagingTargetPath); os.IsNotExist(statErr) {
			klog.V(4).Infof("Staging path %s no longer exists; iSCSI volume %s is already unstaged", stagingTargetPath, volumeID)
			return &csi.NodeUnstageVolumeResponse{}, nil
		}
		return nil, status.Errorf(codes.Internal,
			"cannot determine IQN for iSCSI volume %s at staging path %s; refusing to report successful unstage",
			volumeID, stagingTargetPath)
	}

	// Preserve the real IQN before unmounting so a failed logout can be retried.
	if err := writeISCSIStagingIQN(stagingTargetPath, iqn); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to persist iSCSI staging metadata before unmount: %v", err)
	}

	// Check if mounted and unmount if necessary
	mounted, err := mount.IsMounted(ctx, stagingTargetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to check if staging path is mounted: %v", err)
	}

	if mounted {
		klog.V(4).Infof("Unmounting staging path: %s", stagingTargetPath)
		if err := mount.Unmount(ctx, stagingTargetPath); err != nil {
			return nil, status.Errorf(codes.Internal, "Failed to unmount staging path: %v", err)
		}
	}

	// Logout from the iSCSI target
	server := volumeContext["server"]
	port := volumeContext["port"]
	if port == "" {
		port = "3260"
	}

	params := &iscsiConnectionParams{
		iqn:    iqn,
		server: server,
		port:   port,
	}

	klog.V(4).Infof("Logging out from iSCSI target for volume %s: IQN=%s", volumeID, iqn)
	if err := s.logoutISCSITarget(ctx, params); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to logout iSCSI volume %s (IQN %s): %v", volumeID, iqn, err)
	}
	if err := removeISCSIBlockStagingPath(stagingTargetPath); err != nil {
		return nil, status.Errorf(codes.Internal, "logged out iSCSI volume %s but failed to remove block staging path: %v", volumeID, err)
	}
	if err := removeISCSIStagingIQN(stagingTargetPath); err != nil {
		return nil, status.Errorf(codes.Internal, "logged out iSCSI volume %s but failed to remove staging metadata: %v", volumeID, err)
	}
	klog.Infof("Logged out iSCSI volume %s from target %s", volumeID, iqn)

	return &csi.NodeUnstageVolumeResponse{}, nil
}

func (s *NodeService) resolveISCSIUnstageIQN(ctx context.Context, stagingTargetPath string, volumeContext map[string]string) string {
	if iqn := volumeContext[VolumeContextKeyISCSIIQN]; iqn != "" {
		return iqn
	}

	iqn, err := s.deriveISCSIIQNFromStagingPath(ctx, stagingTargetPath)
	if err != nil {
		klog.Warningf("Failed to derive iSCSI IQN from staging path %s: %v", stagingTargetPath, err)
		return ""
	}
	klog.V(4).Infof("Derived iSCSI IQN from staging path %s: %s", stagingTargetPath, iqn)
	return iqn
}

func (s *NodeService) deriveISCSIIQNFromStagingPath(ctx context.Context, stagingTargetPath string) (string, error) {
	if iqn, err := readISCSIStagingIQN(stagingTargetPath); err == nil && iqn != "" {
		return iqn, nil
	} else if err != nil {
		klog.Warningf("Failed to read iSCSI staging metadata for %s: %v; falling back to session metadata", stagingTargetPath, err)
	}

	devicePath, err := getStagedISCSIDevicePath(ctx, stagingTargetPath)
	if err != nil {
		return "", err
	}

	return s.findISCSIIQNForDevice(ctx, devicePath)
}

func (s *NodeService) findISCSIIQNForDevice(ctx context.Context, devicePath string) (string, error) {
	output, err := s.runISCSIAdm(ctx, iscsiadmQueryTimeout, "-m", "session", "-P", "3")
	if err != nil {
		return "", fmt.Errorf("failed to query iSCSI sessions: %w, output: %s", err, string(output))
	}

	iqn := parseISCSISessionIQNForDevice(string(output), filepath.Base(devicePath))
	if iqn == "" {
		return "", fmt.Errorf("%w for staged device %s", ErrISCSIDeviceNotFound, devicePath)
	}
	return iqn, nil
}

// lookupMountedEntry is mount.MountedEntry; tests replace it to simulate mounts without privileges.
var lookupMountedEntry = mount.MountedEntry

func getStagedISCSIDevicePath(ctx context.Context, stagingTargetPath string) (string, error) {
	entry, mounted, err := lookupMountedEntry(ctx, stagingTargetPath)
	if err != nil {
		// Fail closed: guessing a device here could log out another volume's session.
		return "", fmt.Errorf("mount table lookup failed for %s: %w", stagingTargetPath, err)
	}
	if mounted && strings.HasPrefix(entry.Source, "/dev/") {
		return entry.Source, nil
	}

	resolved, err := filepath.EvalSymlinks(stagingTargetPath)
	if err != nil {
		return "", fmt.Errorf("failed to resolve iSCSI staging path %s: %w", stagingTargetPath, err)
	}
	if !strings.HasPrefix(resolved, "/dev/") {
		return "", fmt.Errorf("%w: %s -> %s", ErrISCSINonDevicePath, stagingTargetPath, resolved)
	}
	return resolved, nil
}

func parseISCSISessionIQNForDevice(output, deviceName string) string {
	deviceName = filepath.Base(deviceName)
	currentIQN := ""

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, iscsiTargetPrefix) {
			targetFields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, iscsiTargetPrefix)))
			currentIQN = ""
			if len(targetFields) > 0 {
				currentIQN = targetFields[0]
			}
			continue
		}
		if currentIQN == "" || !strings.Contains(line, "Attached scsi disk") {
			continue
		}
		if disk, ok := attachedSCSIDisk(line); ok && disk == deviceName {
			return currentIQN
		}
	}

	return ""
}

func iscsiStagingMetadataPath(stagingTargetPath string) string {
	return stagingTargetPath + iscsiStagingMetadataSuffix
}

func writeISCSIStagingIQN(stagingTargetPath, iqn string) error {
	if strings.TrimSpace(iqn) == "" {
		return ErrISCSIEmptyIQN
	}
	if err := os.MkdirAll(filepath.Dir(stagingTargetPath), 0o750); err != nil {
		return err
	}
	return os.WriteFile(iscsiStagingMetadataPath(stagingTargetPath), []byte(iqn+"\n"), 0o600)
}

func readISCSIStagingIQN(stagingTargetPath string) (string, error) {
	data, err := os.ReadFile(iscsiStagingMetadataPath(stagingTargetPath))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	iqn := strings.TrimSpace(string(data))
	if iqn == "" {
		return "", ErrISCSIEmptyIQN
	}
	return iqn, nil
}

func removeISCSIStagingIQN(stagingTargetPath string) error {
	err := os.Remove(iscsiStagingMetadataPath(stagingTargetPath))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func removeISCSIBlockStagingPath(stagingTargetPath string) error {
	info, err := os.Lstat(stagingTargetPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return nil
	}
	return os.Remove(stagingTargetPath)
}

// getISCSIMountOptions merges user-provided mount options with sensible defaults.
func getISCSIMountOptions(userOptions []string) []string {
	if len(userOptions) == 0 {
		return defaultISCSIMountOptions
	}

	// Build a map of user-specified option keys
	userOptionKeys := make(map[string]bool)
	for _, opt := range userOptions {
		key := extractISCSIOptionKey(opt)
		userOptionKeys[key] = true
	}

	// Start with user options, then add defaults that don't conflict
	result := make([]string, 0, len(userOptions)+len(defaultISCSIMountOptions))
	result = append(result, userOptions...)

	for _, defaultOpt := range defaultISCSIMountOptions {
		key := extractISCSIOptionKey(defaultOpt)
		if !userOptionKeys[key] {
			result = append(result, defaultOpt)
		}
	}

	return result
}

// extractISCSIOptionKey extracts the key from a mount option.
func extractISCSIOptionKey(option string) string {
	for i, c := range option {
		if c == '=' {
			return option[:i]
		}
	}
	return option
}
