package upgrade

type UpgradeOutput struct {
	Name   string `json:"name"`
	Info   string `json:"info"`
	Height int64  `json:"height"`
}

type UpgradeInfoOutput struct {
	Binaries map[string]string `json:"binaries"`
}

type UpgradeInfoInput struct {
	Binaries    map[string]string `json:"api_binaries"`
	NodeVersion string            `json:"node_version"`
}
