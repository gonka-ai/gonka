package run

import "trainshard/internal/domain/shared"

var (
	ErrImageMissing     = shared.New("IMAGE_MISSING", shared.ErrValidation, "no image digest in the request")
	ErrImageNotDerived  = shared.New("IMAGE_NOT_DERIVED", shared.ErrValidation, "image is not built on the proposal base image")
	ErrContainerRunning = shared.New("CONTAINER_RUNNING", shared.ErrConflict, "container is running")
	ErrContainerMissing = shared.New("CONTAINER_MISSING", shared.ErrConflict, "container does not exist")
	ErrGPUsExceeded     = shared.New("GPUS_EXCEEDED", shared.ErrValidation, "requested gpus exceed the host limit")
	ErrDiskExceeded     = shared.New("DISK_EXCEEDED", shared.ErrValidation, "requested disk exceeds the host limit")
	ErrSourcesExceeded  = shared.New("SOURCES_EXCEEDED", shared.ErrValidation, "requested outside sources exceed the host limit")
	ErrImagesDiffer     = shared.New("IMAGES_DIFFER", shared.ErrConflict, "nodes hold different images")
	ErrNoNodes          = shared.New("NO_NODES", shared.ErrValidation, "no nodes in the run")
	ErrStatusUnknown    = shared.New("STATUS_UNKNOWN", shared.ErrUnavailable, "a node did not report the image it holds")
	ErrVolumeMissing    = shared.New("VOLUME_MISSING", shared.ErrNotFound, "run has no volume on this node")
	ErrQuotaUnknown     = shared.New("QUOTA_UNKNOWN", shared.ErrUnavailable, "run volume has no readable quota to cap artifacts by")
	ErrArtifactsTooBig  = shared.New("ARTIFACTS_TOO_BIG", shared.ErrConflict, "artifacts do not fit the run's disk quota")
)
