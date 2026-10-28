package nfs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/fenio/tns-csi/pkg/tnsapi"
	"github.com/fenio/tns-csi/tests/e2e/framework"
)

// These specs exercise what the driver does when a TrueNAS replication is interrupted,
// on a real middlewared. The driver's detached clones and snapshots copy data with
// replication.run_onetime; an interrupted receive must never be treated as a finished copy.

// replicationJob is the subset of a core.get_jobs entry these specs read.
type replicationJob struct {
	State     string            `json:"state"`
	Arguments []json.RawMessage `json:"arguments"`
	ID        int               `json:"id"`
}

func (j replicationJob) target() string {
	if len(j.Arguments) == 0 {
		return ""
	}
	var args struct {
		TargetDataset string `json:"target_dataset"`
	}
	if json.Unmarshal(j.Arguments[0], &args) != nil {
		return ""
	}
	return args.TargetDataset
}

func listReplicationJobs(ctx context.Context, c *tnsapi.Client) ([]replicationJob, error) {
	var jobs []replicationJob
	err := c.Call(ctx, "core.get_jobs", []any{
		[]any{[]any{"method", "=", "replication.run_onetime"}},
		map[string]any{"select": []string{"id", "state", "arguments"}},
	}, &jobs)
	return jobs, err
}

func abortJob(ctx context.Context, c *tnsapi.Client, id int) error {
	return c.Call(ctx, "core.job_abort", []any{id}, nil)
}

// writeRandomData fills the pod's volume with sizeMiB of random data and returns its md5.
func writeRandomData(ctx context.Context, f *framework.Framework, pod string, sizeMiB int) string {
	_, err := f.K8s.ExecInPod(ctx, pod, []string{"sh", "-c",
		fmt.Sprintf("dd if=/dev/urandom of=/data/blob bs=1M count=%d 2>/dev/null && sync", sizeMiB)})
	Expect(err).NotTo(HaveOccurred(), "Failed to write random data")
	out, err := f.K8s.ExecInPod(ctx, pod, []string{"sh", "-c", "md5sum /data/blob | cut -d' ' -f1"})
	Expect(err).NotTo(HaveOccurred(), "Failed to checksum random data")
	return strings.TrimSpace(out)
}

var _ = Describe("Interrupted replication", func() {
	var f *framework.Framework

	BeforeEach(func() {
		var err error
		f, err = framework.NewFramework()
		Expect(err).NotTo(HaveOccurred(), "Failed to create framework")
		Expect(f.Setup("nfs")).To(Succeed(), "Failed to setup framework")
		if f.TrueNAS == nil {
			Skip("TrueNAS verifier not configured")
		}
	})

	AfterEach(func() {
		if f != nil {
			f.Teardown()
		}
	})

	// The driver treats a target with receive_resume_token as an incomplete receive and
	// discards it (Dataset.ReceiveIncomplete). This checks that an interrupted
	// replication.run_onetime really leaves that token, so the check can ever fire.
	It("leaves receive_resume_token on the target when a replication is aborted mid-transfer", func() {
		ctx := context.Background()
		client := f.TrueNAS.Client()

		// TrueNAS rejects speed_limit for LOCAL replications ("Speed limit has no sense
		// for local replication"), so the copy is made slow by size instead: 1 GiB took
		// 5-13s on the reference lab, so 6 GiB leaves a wide window to abort mid-transfer.
		By("Creating a source volume with 6 GiB of random data")
		pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
			Name: "resume-token-src", StorageClassName: "tns-csi-nfs", Size: "16Gi",
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
		})
		Expect(err).NotTo(HaveOccurred())
		pod, err := f.CreatePod(ctx, framework.PodOptions{Name: "resume-token-src-pod", PVCName: pvc.Name, MountPath: "/data"})
		Expect(err).NotTo(HaveOccurred())
		Expect(f.K8s.WaitForPodReady(ctx, pod.Name, 2*time.Minute)).To(Succeed())
		writeRandomData(ctx, f, pod.Name, 6144)
		pv, err := f.K8s.GetPVForPVC(ctx, pvc.Name)
		Expect(err).NotTo(HaveOccurred())
		source := f.GetDatasetPathFromPV(pv)
		Expect(source).NotTo(BeEmpty())

		By("Snapshotting the source")
		snapName := "e2e-resume-token"
		_, err = client.CreateSnapshot(ctx, tnsapi.SnapshotCreateParams{Dataset: source, Name: snapName})
		Expect(err).NotTo(HaveOccurred())
		f.Cleanup.Add(func() error { return client.DeleteSnapshot(context.Background(), source+"@"+snapName) })

		target := fmt.Sprintf("%s/e2e-resume-token-%d", f.Config.TrueNASPool, time.Now().Unix())
		f.Cleanup.Add(func() error {
			exists, existsErr := f.TrueNAS.DatasetExists(context.Background(), target)
			if existsErr != nil || !exists {
				return existsErr
			}
			return client.DeleteDataset(context.Background(), target)
		})

		By("Starting a replication with the same parameters the driver uses")
		var jobID int
		err = client.Call(ctx, "replication.run_onetime", []any{map[string]any{
			"direction":                  "PUSH",
			"transport":                  "LOCAL",
			"source_datasets":            []string{source},
			"target_dataset":             target,
			"recursive":                  false,
			"properties":                 true,
			"properties_exclude":         []string{"mountpoint", "sharenfs", "sharesmb", tnsapi.PropertyCSIVolumeName},
			"replicate":                  false,
			"encryption":                 false,
			"name_regex":                 snapName,
			"naming_schema":              []string{},
			"also_include_naming_schema": []string{},
			"retention_policy":           "NONE",
			"readonly":                   "IGNORE",
			"allow_from_scratch":         true,
		}}, &jobID)
		Expect(err).NotTo(HaveOccurred(), "replication.run_onetime failed")
		GinkgoWriter.Printf("replication job %d -> %s\n", jobID, target)

		By("Waiting until data is flowing, then aborting the job")
		Eventually(func() (string, error) {
			status, statusErr := client.GetJobStatus(ctx, jobID)
			if statusErr != nil {
				return "", statusErr
			}
			return status.State, nil
		}, time.Minute, time.Second).Should(Equal("RUNNING"))
		time.Sleep(10 * time.Second) // let a GiB or so arrive, well short of 6 GiB
		status, err := client.GetJobStatus(ctx, jobID)
		Expect(err).NotTo(HaveOccurred())
		Expect(status.State).To(Equal("RUNNING"),
			"replication was no longer running when it should have been mid-transfer (state %s, error %q)", status.State, status.Error)
		Expect(abortJob(ctx, client, jobID)).To(Succeed())
		Eventually(func() (string, error) {
			s, statusErr := client.GetJobStatus(ctx, jobID)
			if statusErr != nil {
				return "", statusErr
			}
			return s.State, nil
		}, 2*time.Minute, time.Second).Should(Or(Equal("ABORTED"), Equal("FAILED")))

		By("Checking the target carries receive_resume_token")
		ds, err := client.Dataset(ctx, target)
		Expect(err).NotTo(HaveOccurred(), "querying the interrupted target failed")
		Expect(ds).NotTo(BeNil(), "an aborted full receive left no dataset at %s", target)
		GinkgoWriter.Printf("receive_resume_token on %s: %v\n", target, ds.ReceiveResumeToken)
		Expect(ds.ReceiveIncomplete()).To(BeTrue(),
			"an aborted receive must leave receive_resume_token set, or the driver cannot tell a partial copy from a finished one")
	})

	// A detached clone copies the snapshot with replication.run_onetime. If that job is
	// aborted, the driver must notice, discard whatever was received, and copy again, so
	// the PVC binds with the full data rather than a partial copy.
	It("recovers a detached clone whose replication is aborted", func() {
		ctx := context.Background()
		client := f.TrueNAS.Client()

		By("Creating a source volume with 1 GiB of random data")
		pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
			Name: "aborted-clone-src", StorageClassName: "tns-csi-nfs", Size: "4Gi",
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
		})
		Expect(err).NotTo(HaveOccurred())
		pod, err := f.CreatePod(ctx, framework.PodOptions{Name: "aborted-clone-src-pod", PVCName: pvc.Name, MountPath: "/data"})
		Expect(err).NotTo(HaveOccurred())
		Expect(f.K8s.WaitForPodReady(ctx, pod.Name, 2*time.Minute)).To(Succeed())
		sum := writeRandomData(ctx, f, pod.Name, 1024)

		By("Snapshotting the source")
		Expect(f.K8s.CreateVolumeSnapshotClass(ctx, "aborted-clone-snapclass", "tns.csi.io", "Delete")).To(Succeed())
		f.Cleanup.Add(func() error { return f.K8s.DeleteVolumeSnapshotClass(context.Background(), "aborted-clone-snapclass") })
		Expect(f.K8s.CreateVolumeSnapshot(ctx, "aborted-clone-snap", pvc.Name, "aborted-clone-snapclass")).To(Succeed())
		f.Cleanup.Add(func() error { return f.K8s.DeleteVolumeSnapshot(context.Background(), "aborted-clone-snap") })
		Expect(f.K8s.WaitForSnapshotReady(ctx, "aborted-clone-snap", 5*time.Minute)).To(Succeed())

		By("Creating a StorageClass that restores snapshots with send/receive")
		scName := "tns-csi-nfs-detached-restore"
		Expect(f.K8s.CreateStorageClassWithParams(ctx, scName, "tns.csi.io", map[string]string{
			"protocol": "nfs", "pool": f.Config.TrueNASPool, "server": f.Config.TrueNASHost,
			"detachedVolumesFromSnapshots": "true",
		})).To(Succeed())
		f.Cleanup.Add(func() error { return f.K8s.DeleteStorageClass(context.Background(), scName) })

		known := map[int]bool{}
		before, err := listReplicationJobs(ctx, client)
		Expect(err).NotTo(HaveOccurred())
		for _, j := range before {
			known[j.ID] = true
		}

		By("Aborting the first replication the driver starts for the restore")
		var (
			mu          sync.Mutex
			abortedID   int
			abortedInto string
		)
		watchCtx, stopWatch := context.WithTimeout(ctx, 5*time.Minute)
		defer stopWatch()
		go func() {
			defer GinkgoRecover()
			for watchCtx.Err() == nil {
				jobs, listErr := listReplicationJobs(watchCtx, client)
				if listErr == nil {
					for _, j := range jobs {
						if known[j.ID] || j.State != "RUNNING" || strings.Contains(j.target(), "/csi-detached-snapshots/") {
							continue
						}
						if abortErr := abortJob(watchCtx, client, j.ID); abortErr == nil {
							mu.Lock()
							abortedID, abortedInto = j.ID, j.target()
							mu.Unlock()
							GinkgoWriter.Printf("aborted replication job %d into %s\n", j.ID, j.target())
							return
						}
					}
				}
				time.Sleep(200 * time.Millisecond)
			}
		}()

		restored := "aborted-clone-restored"
		Expect(f.K8s.CreatePVCFromSnapshot(ctx, restored, "aborted-clone-snap", scName, "4Gi",
			[]corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany})).To(Succeed())
		f.RegisterPVCCleanup(restored)

		Eventually(func() int {
			mu.Lock()
			defer mu.Unlock()
			return abortedID
		}, 5*time.Minute, time.Second).ShouldNot(BeZero(),
			"the restore's replication finished before it could be aborted; write more data")

		By("Waiting for the restore to bind and mount despite the aborted copy")
		Expect(f.K8s.WaitForPVCBound(ctx, restored, 10*time.Minute)).To(Succeed())
		rpod, err := f.CreatePod(ctx, framework.PodOptions{Name: "aborted-clone-restored-pod", PVCName: restored, MountPath: "/data"})
		Expect(err).NotTo(HaveOccurred())
		Expect(f.K8s.WaitForPodReady(ctx, rpod.Name, 3*time.Minute)).To(Succeed())

		By("Verifying the restored data is the full copy")
		out, err := f.K8s.ExecInPod(ctx, rpod.Name, []string{"sh", "-c", "md5sum /data/blob | cut -d' ' -f1"})
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.TrimSpace(out)).To(Equal(sum), "restored data differs from the source")

		By("Verifying the driver started a new replication after the abort")
		jobs, err := listReplicationJobs(ctx, client)
		Expect(err).NotTo(HaveOccurred())
		var succeededAfter bool
		for _, j := range jobs {
			if j.ID > abortedID && j.target() == abortedInto && j.State == "SUCCESS" {
				succeededAfter = true
			}
		}
		Expect(succeededAfter).To(BeTrue(), "no successful replication into %s after aborted job %d", abortedInto, abortedID)
	})
})
