package driver

import (
	"context"
	"testing"

	"github.com/fenio/tns-csi/pkg/tnsapi"
)

// Creation and expansion size NFS/SMB datasets with refquota. Adoption must use the
// same property: setting quota instead leaves the effective refquota at its old value
// while the reported capacity says the volume grew.
func TestExpandAdoptedVolumeUsesRefQuotaForFilesystems(t *testing.T) {
	for _, protocol := range []string{ProtocolNFS, ProtocolSMB} {
		t.Run(protocol, func(t *testing.T) {
			var got tnsapi.DatasetUpdateParams
			mock := &mockAPIClient{
				updateDatasetFunc: func(_ context.Context, _ string, params tnsapi.DatasetUpdateParams) (*tnsapi.Dataset, error) {
					got = params
					return &tnsapi.Dataset{}, nil
				},
			}
			s := NewControllerService(mock, nil, "")
			const want = int64(20 * 1024 * 1024 * 1024)
			err := s.expandAdoptedVolume(context.Background(), &tnsapi.DatasetWithProperties{Dataset: tnsapi.Dataset{ID: "tank/csi/vol"}}, protocol, want)
			if err != nil {
				t.Fatalf("expandAdoptedVolume() error = %v", err)
			}
			if got.RefQuota == nil || *got.RefQuota != want {
				t.Errorf("RefQuota = %v, want %d", got.RefQuota, want)
			}
			if got.Quota != nil {
				t.Errorf("Quota = %d, want unset (creation and expansion use refquota)", *got.Quota)
			}
			if got.Volsize != nil {
				t.Errorf("Volsize set for a filesystem dataset")
			}
		})
	}
	t.Run("zvol uses volsize", func(t *testing.T) {
		var got tnsapi.DatasetUpdateParams
		mock := &mockAPIClient{
			updateDatasetFunc: func(_ context.Context, _ string, params tnsapi.DatasetUpdateParams) (*tnsapi.Dataset, error) {
				got = params
				return &tnsapi.Dataset{}, nil
			},
		}
		s := NewControllerService(mock, nil, "")
		const want = int64(1 << 30)
		if err := s.expandAdoptedVolume(context.Background(), &tnsapi.DatasetWithProperties{Dataset: tnsapi.Dataset{ID: "tank/csi/vol"}}, ProtocolISCSI, want); err != nil {
			t.Fatalf("expandAdoptedVolume() error = %v", err)
		}
		if got.Volsize == nil || *got.Volsize != want || got.RefQuota != nil || got.Quota != nil {
			t.Errorf("params = %+v, want only Volsize=%d", got, want)
		}
	})
}
