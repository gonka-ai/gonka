package broker

type InferenceNode struct {
	Url           string   `koanf:"url"`
	Models        []string `koanf:"models"`
	Id            string   `koanf:"id"`
	MaxConcurrent int      `koanf:"max_concurrent"`
}
