package types

type SubSystem uint8

const (
	Payments SubSystem = iota
	EpochGroup
	PoC
	Tokenomics
	Pricing         = 4
	Validation      = 5
	Settle          = 6
	System          = 7
	Claims          = 8
	Inferences      = 9
	Participants    = 10
	Messages        = 11
	Nodes           = 12
	Config          = 13
	EventProcessing = 14
	Upgrades        = 15
	Server          = 16
	Training        = 17
	Stages          = 18
	Balances        = 19
	Testing         = 255
)

func (s SubSystem) String() string {
	switch s {
	case Payments:
		return "Payments"
	case EpochGroup:
		return "EpochGroup"
	case PoC:
		return "PoC"
	case Tokenomics:
		return "Tokenomics"
	case Pricing:
		return "Pricing"
	case Validation:
		return "Validation"
	case Settle:
		return "Settle"
	case System:
		return "System"
	case Claims:
		return "Claims"
	case Inferences:
		return "Inferences"
	case Participants:
		return "Participants"
	case Messages:
		return "Messages"
	case Nodes:
		return "Nodes"
	case Config:
		return "Config"
	case EventProcessing:
		return "EventProcessing"
	case Upgrades:
		return "Upgrades"
	case Server:
		return "Server"
	case Stages:
		return "Stages"
	case Balances:
		return "Balances"
	case Testing:
		return "Testing"
	default:
		return "Unknown"
	}
}

type InferenceLogger interface {
	LogInfo(msg string, subSystem SubSystem, keyvals ...interface{})
	LogError(msg string, subSystem SubSystem, keyvals ...interface{})
	LogWarn(msg string, subSystem SubSystem, keyvals ...interface{})
	LogDebug(msg string, subSystem SubSystem, keyvals ...interface{})
}
