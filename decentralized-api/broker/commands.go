package broker

import (
	"decentralized-api/apiconfig"
	"github.com/productscience/inference/x/inference/types"
	"time"
)

type Command interface {
	GetResponseChannelCapacity() int
}

type LockAvailableNode struct {
	Model                string
	Version              string
	AcceptEarlierVersion bool
	Response             chan *Node
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
	Node     apiconfig.InferenceNodeConfig
	Response chan *apiconfig.InferenceNodeConfig
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

type ReconcileNodesCommand struct {
	Response chan bool
}

func (c ReconcileNodesCommand) GetResponseChannelCapacity() int {
	return cap(c.Response)
}

type SetNodesActualStatusCommand struct {
	StatusUpdates []StatusUpdate
	Response      chan bool
}

type StatusUpdate struct {
	NodeId     string
	PrevStatus types.HardwareNodeStatus
	NewStatus  types.HardwareNodeStatus
	Timestamp  time.Time
}

func (c SetNodesActualStatusCommand) GetResponseChannelCapacity() int {
	return cap(c.Response)
}

type InferenceUpAllCommand struct {
	Response chan bool
}

func NewInferenceUpAllCommand() InferenceUpAllCommand {
	return InferenceUpAllCommand{
		Response: make(chan bool, 2),
	}
}

func (c InferenceUpAllCommand) GetResponseChannelCapacity() int {
	return cap(c.Response)
}
