package filters

import "strings"

func runPipeline(document *Document, options Options) (Result, error) {
	routedModel := resolveRoutedModel(document, options.RoutedModel)
	profile := options.Profiles.For(routedModel)

	unwrapExtraBody(document)
	if err := rejectUnknownParameters(document); err != nil {
		return Result{}, err
	}
	if err := applyStage(StagePreValidation, document, profile); err != nil {
		return Result{}, err
	}
	if err := validateStopTokenIDs(document, options.StopTokenVocabulary, routedModel); err != nil {
		return Result{}, err
	}
	if err := normalizeMessages(document); err != nil {
		return Result{}, err
	}
	if err := validateMessages(document); err != nil {
		return Result{}, err
	}
	view, err := decodeRequestView(document)
	if err != nil {
		return Result{}, err
	}
	applyOutputTokenLimits(document, &view, options, routedModel, profile)
	// Read before StagePostLimits: that stage caps top_logprobs, and the intent is what the client asked for.
	logprobs := decodeLogprobIntent(document)
	if err := applyStage(StagePostLimits, document, profile); err != nil {
		return Result{}, err
	}
	if err := syncRequestView(document, &view); err != nil {
		return Result{}, err
	}
	usage := decodeUsageIntent(document)
	if !options.KeepClientStream {
		forceUpstreamStreaming(document)
	}
	body, err := document.Marshal()
	if err != nil {
		return Result{}, err
	}
	return Result{
		Body:                    body,
		Model:                   view.Model,
		ClientStream:            view.Stream,
		ClientUsage:             usage,
		MaxTokens:               view.MaxTokens,
		MaxCompletionTokens:     view.MaxCompletionTokens,
		Logprobs:                logprobs,
		EmptyAnswerCountsTokens: profile.emptyAnswerCountsTokens(),
	}, nil
}

// Shared rather than built per request: the document is marshalled and dropped straight after.
var forcedStreamOptions = map[string]any{"include_usage": true}

// forceUpstreamStreaming makes every host request a streamed one. See README.md, "Streaming is forced upstream".
func forceUpstreamStreaming(document *Document) {
	document.Set("stream", true)
	document.Set("stream_options", forcedStreamOptions)
}

func decodeUsageIntent(document *Document) bool {
	options, present, isObject := document.ObjectField("stream_options")
	if !present || !isObject {
		return false
	}
	asked, isBool := options["include_usage"].(bool)
	return isBool && asked
}

func resolveRoutedModel(document *Document, fallback string) string {
	if raw, ok := document.Get("model"); ok {
		if modelName, isString := raw.(string); isString {
			if trimmed := strings.TrimSpace(modelName); trimmed != "" {
				return trimmed
			}
		}
	}
	return fallback
}

func applyStage(stage Stage, document *Document, profile *Profile) error {
	for _, spec := range parameterTable {
		for _, rule := range spec.Rules {
			if rule.Stage != stage || rule.Apply == nil {
				continue
			}
			ruleContext := RuleContext{
				Document: document,
				Param:    spec.Name,
				Profile:  profile,
			}
			if err := rule.Apply(ruleContext); err != nil {
				return err
			}
		}
	}
	return nil
}
