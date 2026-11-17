package filters

func clampToProfileLimit(limitOf func(SamplingLimits) *float64) RuleFunc {
	return func(ctx RuleContext) error {
		limit := limitOf(ctx.Profile.samplingLimits())
		if limit == nil {
			return nil
		}
		number, ok := sanitizeFloatField(ctx.Document, ctx.Param)
		if ok && number > *limit {
			ctx.Document.Set(ctx.Param, *limit)
		}
		return nil
	}
}

func topKToProfileLimit() RuleFunc {
	return func(ctx RuleContext) error {
		limit := ctx.Profile.samplingLimits().TopKMax
		if limit == nil {
			return nil
		}
		number, ok := sanitizeFloatField(ctx.Document, ctx.Param)
		if ok && (number == topKDisabled || number > float64(*limit)) {
			ctx.Document.Set(ctx.Param, *limit)
		}
		return nil
	}
}

func temperatureLimit(limits SamplingLimits) *float64       { return limits.TemperatureMax }
func topPLimit(limits SamplingLimits) *float64              { return limits.TopPMax }
func minPLimit(limits SamplingLimits) *float64              { return limits.MinPMax }
func repetitionPenaltyLimit(limits SamplingLimits) *float64 { return limits.RepetitionPenaltyMax }
