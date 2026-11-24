package config

import "fmt"

const (
	minChatFrameEvents = 1
	maxChatFrameEvents = 64
)

// ChatFrames selects the executor's peer RPC event and signature schedule.
type ChatFrames struct {
	Events   int64
	Separate bool
}

func (c ChatFrames) validate() error {
	if c.Events < minChatFrameEvents || c.Events > maxChatFrameEvents {
		return fmt.Errorf("chat_frame_events must be between %d and %d", minChatFrameEvents, maxChatFrameEvents)
	}
	return nil
}
