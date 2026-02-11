package types

type SubSystem uint8

const (
	Payments SubSystem = iota
	EpochGroup
	PoC
	Tokenomics
	Pricing
	Validation
	Settle
	System
	Claims
	Inferences
	Participants
	Messages
	Nodes
	Config
	EventProcessing
	Upgrades
	Server
	Training
	Stages
	Balances
	Stat
	Pruning
	BLS
	ValidationRecovery
	Allocation
	PayloadStorage
	Testing = 255
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
	case Stat:
		return "Stat"
	case Testing:
		return "Testing"
	case Pruning:
		return "Pruning"
	case BLS:
		return "BLS"
	case ValidationRecovery:
		return "ValidationRecovery"
	case Allocation:
		return "Allocation"
	case PayloadStorage:
		return "PayloadStorage"
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
