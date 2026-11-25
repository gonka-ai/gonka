package filters

const (
	kimiModelID       = "moonshotai/Kimi-K2.6"
	minimaxModelID    = "MiniMaxAI/MiniMax-M2.7"
	glm53FlashModelID = "zai-org/GLM-5.3-Flash"

	kimiOutputTokensFloor uint64 = 16

	presetKimiForTest     PresetName = "kimi"
	presetMinimaxForTest  PresetName = "minimax"
	presetGLMFlashForTest PresetName = "glm_flash"
)

var (
	kimiProfile       = BuiltInProfileFor(kimiModelID)
	minimaxProfile    = BuiltInProfileFor(minimaxModelID)
	glm53FlashProfile = BuiltInProfileFor(glm53FlashModelID)
)
