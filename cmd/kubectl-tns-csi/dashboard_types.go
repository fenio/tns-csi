package main

// Type aliases for shared types from pkg/dashboard.
// This allows the kubectl plugin to use the same types as the in-cluster dashboard
// without duplicating definitions. Go type aliases are transparent — no conversion needed.

import "github.com/fenio/tns-csi/pkg/dashboard"

// Data types used across multiple kubectl commands.
type (
	VolumeInfo             = dashboard.VolumeInfo
	SnapshotInfo           = dashboard.SnapshotInfo
	CloneInfo              = dashboard.CloneInfo
	UnmanagedVolume        = dashboard.UnmanagedVolume
	HealthStatus           = dashboard.HealthStatus
	VolumeHealth           = dashboard.VolumeHealth
	HealthReport           = dashboard.HealthReport
	HealthSummary          = dashboard.HealthSummary
	K8sVolumeBinding       = dashboard.K8sVolumeBinding
	K8sEnrichmentResult    = dashboard.K8sEnrichmentResult
	VolumeDetails          = dashboard.VolumeDetails
	NFSShareDetails        = dashboard.NFSShareDetails
	NVMeOFSubsystemDetails = dashboard.NVMeOFSubsystemDetails
	SMBShareDetails        = dashboard.SMBShareDetails
	ISCSITargetDetails     = dashboard.ISCSITargetDetails
	MetricsSummary         = dashboard.MetricsSummary
	DashboardData          = dashboard.Data
	SummaryData            = dashboard.SummaryData
	PaginationParams       = dashboard.PaginationParams
	PaginatedVolumes       = dashboard.PaginatedVolumes
	PaginatedSnapshots     = dashboard.PaginatedSnapshots
	PaginatedClones        = dashboard.PaginatedClones
	PaginatedUnmanaged     = dashboard.PaginatedUnmanaged
)
