package harness

// CountGatewayInferenceStatus counts records in the given status.
func CountGatewayInferenceStatus(inferences map[string]GatewayDebugInference, status string) int {
	count := 0
	for _, record := range inferences {
		if record.Status == status {
			count++
		}
	}
	return count
}
