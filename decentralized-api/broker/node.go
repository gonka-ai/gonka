package broker

import "fmt"

type InferenceNode struct {
	Host          string   `koanf:"host" json:"host"`
	InferencePort int      `koanf:"inference_port" json:"inference_port"`
	PoCPort       int      `koanf:"poc_port" json:"poc_port"`
	Models        []string `koanf:"models" json:"models"`
	Id            string   `koanf:"id" json:"id"`
	MaxConcurrent int      `koanf:"max_concurrent" json:"max_concurrent"`
}

func (n *InferenceNode) InferenceUrl() string {
	return fmt.Sprintf("http://%s:%d", n.Host, n.InferencePort)
}

func (n *InferenceNode) PoCUrl() string {
	return fmt.Sprintf("http://%s:%d", n.Host, n.PoCPort)
}
