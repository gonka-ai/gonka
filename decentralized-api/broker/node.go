package broker

type InferenceNode struct {
	Url           string   `koanf:"url" json:"url"`
	Models        []string `koanf:"models" json:"models"`
	Id            string   `koanf:"id" json:"id"`
	MaxConcurrent int      `koanf:"max_concurrent" json:"max_concurrent"`
}
