package inference

func (am AppModule) SendNewValidatorWeightsToStaking() {
	// PRTODO: Implement

	// TODO: You probably should also set new weight here for Participants?
	//   Or should we do it as soon as we receive nonces?

	// TODO: We should also delete/mark inactive any participants that failed to provide weight

	// TODO: We should also delete any weights from last epochs
}
