package mlnodeclient

// Embedding types

// EmbedRequest is sent to the ML-node /api/v1/embed endpoint.
type EmbedRequest struct {
	Text string `json:"text"`
}

// EmbedResponse is returned by the ML-node /api/v1/embed endpoint.
type EmbedResponse struct {
	Embedding  []float32 `json:"embedding"`
	Dimensions int       `json:"dimensions"`
	Model      string    `json:"model"`
}

// GPU-related types

type GPUDevice struct {
	Index              int     `json:"index"`
	Name               string  `json:"name"`
	TotalMemoryMB      *int    `json:"total_memory_mb"`
	FreeMemoryMB       *int    `json:"free_memory_mb"`
	UsedMemoryMB       *int    `json:"used_memory_mb"`
	UtilizationPercent *int    `json:"utilization_percent"`
	TemperatureC       *int    `json:"temperature_c"`
	IsAvailable        bool    `json:"is_available"`
	ErrorMessage       *string `json:"error_message"`
}

type GPUDevicesResponse struct {
	Devices []GPUDevice `json:"devices"`
	Count   int         `json:"count"`
}

type DriverInfo struct {
	DriverVersion     string `json:"driver_version"`
	CudaDriverVersion string `json:"cuda_driver_version"`
	NvmlVersion       string `json:"nvml_version"`
}

// Model management types

type Model struct {
	HfRepo   string  `json:"hf_repo"`
	HfCommit *string `json:"hf_commit"`
}

type ModelStatus string

const (
	ModelStatusDownloaded  ModelStatus = "DOWNLOADED"
	ModelStatusDownloading ModelStatus = "DOWNLOADING"
	ModelStatusNotFound    ModelStatus = "NOT_FOUND"
	ModelStatusPartial     ModelStatus = "PARTIAL"
)

type DownloadProgress struct {
	StartTime      float64 `json:"start_time"`
	ElapsedSeconds float64 `json:"elapsed_seconds"`
}

type ModelStatusResponse struct {
	Model        Model             `json:"model"`
	Status       ModelStatus       `json:"status"`
	Progress     *DownloadProgress `json:"progress"`
	ErrorMessage *string           `json:"error_message"`
}

type DownloadStartResponse struct {
	TaskId string      `json:"task_id"`
	Status ModelStatus `json:"status"`
	Model  Model       `json:"model"`
}

type DeleteResponse struct {
	Status string `json:"status"`
	Model  Model  `json:"model"`
}

type ModelListItem struct {
	Model  Model       `json:"model"`
	Status ModelStatus `json:"status"`
}

type ModelListResponse struct {
	Models []ModelListItem `json:"models"`
}

type DiskSpaceInfo struct {
	CacheSizeGB float64 `json:"cache_size_gb"`
	AvailableGB float64 `json:"available_gb"`
	CachePath   string  `json:"cache_path"`
}
