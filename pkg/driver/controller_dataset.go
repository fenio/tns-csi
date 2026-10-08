package driver

import (
	"context"

	"github.com/fenio/tns-csi/pkg/tnsapi"
)

// queryExactDataset filters a prefix listing by dataset ID before callers use it
// for volume identity or existence. Prefix siblings and child datasets must never
// be adopted as the requested volume, regardless of the order returned by TrueNAS.
func (s *ControllerService) queryExactDataset(ctx context.Context, datasetID string) ([]tnsapi.Dataset, error) {
	datasets, err := s.apiClient.QueryAllDatasets(ctx, datasetID)
	if err != nil {
		return nil, err
	}
	for _, dataset := range datasets {
		if dataset.ID == datasetID {
			return []tnsapi.Dataset{dataset}, nil
		}
	}
	return nil, nil
}
