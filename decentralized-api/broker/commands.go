package broker

import "decentralized-api/apiconfig"

type Command interface {
	GetResponseChannelCapacity() int
}

type LockAvailableNode struct {
	Model    string
	Response chan *apiconfig.InferenceNode
}

func (g LockAvailableNode) GetResponseChannelCapacity() int {
	return cap(g.Response)
}

type ReleaseNode struct {
	NodeId   string
	Outcome  InferenceResult
	Response chan bool
}

func (r ReleaseNode) GetResponseChannelCapacity() int {
	return cap(r.Response)
}

type RegisterNode struct {
	Node     apiconfig.InferenceNode
	Response chan apiconfig.InferenceNode
}

func (r RegisterNode) GetResponseChannelCapacity() int {
	return cap(r.Response)
}

type GetNodesCommand struct {
	Response chan []NodeResponse
}

func (g GetNodesCommand) GetResponseChannelCapacity() int {
	return cap(g.Response)
}

type RemoveNode struct {
	NodeId   string
	Response chan bool
}

func (r RemoveNode) GetResponseChannelCapacity() int {
	return cap(r.Response)
}

type InferenceResult interface {
	IsSuccess() bool
	GetMessage() string
}

type InferenceSuccess struct {
	Response interface{}
}

type InferenceError struct {
	Message string
	IsFatal bool
}

func (i InferenceSuccess) IsSuccess() bool {
	return true
}

func (i InferenceSuccess) GetMessage() string {
	return "Success"
}

func (i InferenceError) IsSuccess() bool {
	return false
}

func (i InferenceError) GetMessage() string {
	return i.Message
}

type SyncNodesCommand struct {
	Response chan bool
}

func NewSyncNodesCommand() SyncNodesCommand {
	return SyncNodesCommand{
		Response: make(chan bool, 2),
	}
}

func (s SyncNodesCommand) GetResponseChannelCapacity() int {
	return cap(s.Response)
}

type LockNodesForTrainingCommand struct {
	NodeIds []string
	// FIXME: maybe add description which exact nodes were busy?
	Response chan bool
}

func NewLockNodesForTrainingCommand(nodeIds []string) LockNodesForTrainingCommand {
	return LockNodesForTrainingCommand{
		NodeIds:  nodeIds,
		Response: make(chan bool, 2),
	}
}

func (c LockNodesForTrainingCommand) GetResponseChannelCapacity() int {
	return cap(c.Response)
}

type StartPoC struct {
	Response chan bool
}

type StartTrainingCommand struct {
	masterNodeAddress string
	worldSize         int
	nodeRanks         map[string]int
	Response          chan bool
}

func NewStartTrainingCommand(masterNodeAddress string, worldSize int, nodeRanks map[string]int) StartTrainingCommand {
	return StartTrainingCommand{
		masterNodeAddress: masterNodeAddress,
		worldSize:         worldSize,
		nodeRanks:         nodeRanks,
		Response:          make(chan bool, 2),
	}
}

func (c StartTrainingCommand) GetResponseChannelCapacity() int {
	return cap(c.Response)
}
