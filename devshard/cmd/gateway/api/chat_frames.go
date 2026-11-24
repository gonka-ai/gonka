package api

import (
	"devshard/cmd/gateway/config"
	"devshard/host"
	"devshard/user"
)

type chatFrameNarrator interface {
	ChatFramesChecked(escrowID, participant string, nonce uint64, check host.ChatFrameCheck)
}

// WithChatFrameNarrator returns a dispatch adapter that reports frame checks.
func (s Sessions) WithChatFrameNarrator(narrator chatFrameNarrator) Sessions {
	s.chatFrames = narrator
	return s
}

func newChatFrameSchedule(configuration config.ChatFrames) *host.ChatFrameSchedule {
	return &host.ChatFrameSchedule{Every: uint32(configuration.Events), Separate: configuration.Separate}
}

func (t escrowTarget) reportChatFrames(prepared *user.PreparedInference, check host.ChatFrameCheck) {
	if t.chatFrames != nil {
		t.chatFrames.ChatFramesChecked(t.escrowID, t.session.HostLabel(prepared.HostIdx()), prepared.Nonce(), check)
	}
}
