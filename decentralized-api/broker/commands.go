package broker

type Command interface {
	GetResponseChannelCapacity() int
}

type LockAvailableNode struct {
	Model    string
	Response chan *InferenceNode
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
	Node     InferenceNode
	Response chan InferenceNode
}

func (r RegisterNode) GetResponseChannelCapacity() int {
	return cap(r.Response)
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

func (i InferenceError) IsSuccess() bool {
	return false
}
