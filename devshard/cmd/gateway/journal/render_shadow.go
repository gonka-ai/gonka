package journal

import (
	"devshard/cmd/gateway/internal/logkey"
	"devshard/cmd/gateway/shadow"
)

const shadowProbeFieldSlots = 20

// ShadowProbed writes how a probe to a shadow participant ended, the line an operator reads to decide whether to promote it to trust. See ../docs/runbook.md, "The participants setting".
func (j *Journal) ShadowProbed(result shadow.Result) {
	j.emitLine(KindShadowProbe, func(lines logSink) { renderShadowProbe(lines, result) })
}

func renderShadowProbe(lines logSink, result shadow.Result) {
	fields := make([]any, 0, shadowProbeFieldSlots)
	fields = append(fields,
		logkey.Escrow, result.Escrow, logkey.Nonce, result.Nonce,
		logkey.Host, logkey.ShortHost(result.Participant), logkey.Model, result.Model,
		logkey.Outcome, result.Outcome, logkey.DurationMS, result.Total.Milliseconds(),
		logkey.OutputTokens, result.OutputTokens)
	if result.Streamed {
		fields = append(fields, logkey.FirstTokenMS, result.FirstToken.Milliseconds())
	}
	if result.Err != nil {
		fields = append(fields, logkey.Error, result.Err)
		lines.Warn("shadow probe failed", fields...)
		return
	}
	lines.Info("shadow probe served", fields...)
}
