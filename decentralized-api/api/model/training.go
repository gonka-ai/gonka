package model

type StartTrainingDto struct {
	HardwareResources []HardwareResourcesDto `json:"hardware_resources"`
	Config            TrainingConfigDto      `json:"config"`
}

type HardwareResourcesDto struct {
	Type  string `json:"type"`
	Count uint32 `json:"count"`
}

type TrainingConfigDto struct {
	Datasets              TrainingDatasetsDto `json:"datasets"`
	NumUocEstimationSteps uint32              `json:"num_uoc_estimation_steps"`
}

type TrainingDatasetsDto struct {
	Train string `json:"train"`
	Test  string `json:"test"`
}
