// Package scale contains E2E tests that validate CSI operations work correctly
// when TrueNAS has a large number of non-CSI-managed datasets, zvols, snapshots,
// and NFS shares. This simulates a real-world environment where users have
// pre-existing data on the same pool used by the CSI driver.
//
// This test suite is intended to be run manually via the "Scale Tests" workflow,
// not as part of regular CI. It populates TrueNAS with noise data, runs CSI
// operations, verifies they work correctly, and cleans up.
package scale

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/klog/v2"

	"github.com/fenio/tns-csi/pkg/tnsapi"
	"github.com/fenio/tns-csi/tests/e2e/framework"
)

const (
	defaultNoiseDatasetCount = 30
	defaultNoiseZvolCount    = 10
	snapshotsPerDataset      = 2
	nfsShareCount            = 5
)

var (
	noiseVerifier *framework.TrueNASVerifier
	noiseParent   string // Parent dataset path for all noise data
	noisePool     string

	// Actual counts (may be overridden by env vars).
	actualDatasetCount int
	actualZvolCount    int
)

func TestScale(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Scale E2E Suite")
}

var _ = BeforeSuite(func() {
	err := framework.SetupSuite("nfs")
	Expect(err).NotTo(HaveOccurred(), "Failed to set up CSI driver")

	createNoiseData()
})

var _ = AfterSuite(func() {
	cleanupNoiseData()
	framework.TeardownSuite()
})

// getEnvInt reads an integer from an environment variable, returning defaultVal if unset.
func getEnvInt(key string, defaultVal int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultVal
}

// createNoiseData populates TrueNAS with non-CSI-managed resources.
// All noise data goes under a single parent dataset for easy cleanup.
func createNoiseData() {
	cfg, err := framework.NewConfig()
	Expect(err).NotTo(HaveOccurred())

	noisePool = cfg.TrueNASPool
	actualDatasetCount = getEnvInt("NOISE_DATASET_COUNT", defaultNoiseDatasetCount)
	actualZvolCount = getEnvInt("NOISE_ZVOL_COUNT", defaultNoiseZvolCount)

	noiseVerifier, err = framework.NewTrueNASVerifier(cfg.TrueNASHost, cfg.TrueNASAPIKey)
	Expect(err).NotTo(HaveOccurred(), "Failed to connect to TrueNAS for noise creation")

	client := noiseVerifier.Client()
	ctx := context.Background()

	noiseParent = fmt.Sprintf("%s/e2e-noise-%d", noisePool, time.Now().UnixNano())
	klog.Infof("Creating noise data under %s (%d datasets, %d zvols)",
		noiseParent, actualDatasetCount, actualZvolCount)

	// Parent dataset
	_, err = client.CreateDataset(ctx, tnsapi.DatasetCreateParams{
		Name: noiseParent,
		Type: "FILESYSTEM",
	})
	Expect(err).NotTo(HaveOccurred(), "Failed to create noise parent dataset")

	// Filesystem datasets with snapshots
	fsParent := noiseParent + "/datasets"
	_, err = client.CreateDataset(ctx, tnsapi.DatasetCreateParams{
		Name: fsParent,
		Type: "FILESYSTEM",
	})
	Expect(err).NotTo(HaveOccurred())

	klog.Infof("Creating %d noise filesystem datasets with %d snapshots each",
		actualDatasetCount, snapshotsPerDataset)
	for i := 1; i <= actualDatasetCount; i++ {
		dsName := fmt.Sprintf("%s/ds-%03d", fsParent, i)
		_, dsErr := client.CreateDataset(ctx, tnsapi.DatasetCreateParams{
			Name: dsName,
			Type: "FILESYSTEM",
		})
		Expect(dsErr).NotTo(HaveOccurred(), "Failed to create noise dataset %s", dsName)

		for j := 1; j <= snapshotsPerDataset; j++ {
			_, snapErr := client.CreateSnapshot(ctx, tnsapi.SnapshotCreateParams{
				Dataset: dsName,
				Name:    fmt.Sprintf("snap-%03d", j),
			})
			Expect(snapErr).NotTo(HaveOccurred(), "Failed to create snapshot for %s", dsName)
		}
	}

	// NFS shares on first N datasets
	klog.Infof("Creating %d noise NFS shares", nfsShareCount)
	for i := 1; i <= nfsShareCount; i++ {
		sharePath := fmt.Sprintf("/mnt/%s/ds-%03d", fsParent, i)
		_, shareErr := client.CreateNFSShare(ctx, tnsapi.NFSShareCreateParams{
			Path:    sharePath,
			Enabled: true,
			Comment: fmt.Sprintf("e2e-noise-share-%d", i),
		})
		Expect(shareErr).NotTo(HaveOccurred(), "Failed to create noise NFS share for %s", sharePath)
	}

	// Zvols with snapshots
	zvolParent := noiseParent + "/zvols"
	_, err = client.CreateDataset(ctx, tnsapi.DatasetCreateParams{
		Name: zvolParent,
		Type: "FILESYSTEM",
	})
	Expect(err).NotTo(HaveOccurred())

	klog.Infof("Creating %d noise zvols with %d snapshots each", actualZvolCount, snapshotsPerDataset)
	sparse := true
	for i := 1; i <= actualZvolCount; i++ {
		zvolName := fmt.Sprintf("%s/zvol-%03d", zvolParent, i)
		_, zvolErr := client.CreateZvol(ctx, tnsapi.ZvolCreateParams{
			Name:    zvolName,
			Type:    "VOLUME",
			Volsize: 1073741824, // 1 GiB
			Sparse:  &sparse,
		})
		Expect(zvolErr).NotTo(HaveOccurred(), "Failed to create noise zvol %s", zvolName)

		for j := 1; j <= snapshotsPerDataset; j++ {
			_, snapErr := client.CreateSnapshot(ctx, tnsapi.SnapshotCreateParams{
				Dataset: zvolName,
				Name:    fmt.Sprintf("snap-%03d", j),
			})
			Expect(snapErr).NotTo(HaveOccurred(), "Failed to create snapshot for %s", zvolName)
		}
	}

	totalSnapshots := (actualDatasetCount + actualZvolCount) * snapshotsPerDataset
	totalResources := actualDatasetCount + actualZvolCount + totalSnapshots + nfsShareCount
	klog.Infof("Noise data creation complete: %d resources (%d datasets, %d zvols, %d snapshots, %d NFS shares)",
		totalResources, actualDatasetCount, actualZvolCount, totalSnapshots, nfsShareCount)
}

// cleanupNoiseData removes all noise data. NFS shares first, then recursive dataset delete.
func cleanupNoiseData() {
	if noiseVerifier == nil || noiseParent == "" {
		return
	}
	defer noiseVerifier.Close()

	ctx := context.Background()
	klog.Infof("Cleaning up noise data under %s", noiseParent)

	// Delete NFS shares first (they reference dataset paths)
	fsParent := noiseParent + "/datasets"
	for i := 1; i <= nfsShareCount; i++ {
		sharePath := fmt.Sprintf("/mnt/%s/ds-%03d", fsParent, i)
		if err := noiseVerifier.DeleteNFSShare(ctx, sharePath); err != nil {
			klog.Warningf("Failed to delete noise NFS share %s: %v", sharePath, err)
		}
	}

	// Delete parent dataset recursively (handles all children, snapshots)
	if err := noiseVerifier.Client().DeleteDataset(ctx, noiseParent); err != nil {
		klog.Errorf("Failed to delete noise parent dataset %s: %v", noiseParent, err)
	} else {
		klog.Infof("Noise data cleanup complete")
	}
}
