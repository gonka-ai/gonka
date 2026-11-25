package journal

import (
	"devshard/cmd/gateway/internal/logkey"
	"devshard/host"
)

// ChatFramesChecked reports verification without changing the attempt's outcome.
func (j *Journal) ChatFramesChecked(escrowID, participant string, nonce uint64, check host.ChatFrameCheck) {
	if check.Frames == noChatFrames && check.Invalid == noChatFrameFailure {
		return
	}
	j.emitLine(KindChatFramesChecked, func(lines logSink) {
		fields := []any{
			logkey.Escrow, escrowID, logkey.Nonce, nonce, logkey.Host, logkey.ShortHost(participant),
			logkey.ChatFrames, check.Frames, logkey.ChatFramesSigned, check.Signed,
		}
		if check.Invalid != noChatFrameFailure {
			lines.Warn(chatFrameSignatureInvalid, append(fields, logkey.Error, check.Invalid)...)
			return
		}
		lines.Info(chatFramesChecked, fields...)
	})
}
