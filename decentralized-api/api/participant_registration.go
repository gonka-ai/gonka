package api

type SubmitUnfundedNewParticipantDto struct {
	Address      string   `json:"address"`
	Url          string   `json:"url"`
	Models       []string `json:"models"`
	ValidatorKey string   `json:"validator_key"`
	PubKey       string   `json:"pub_key"`
	WorkerKey    string   `json:"worker_key"`
}
