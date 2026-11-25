package app

type labelOnlyAdmission struct{}

func (labelOnlyAdmission) AllowRequest(string, string) error { return nil }

func (labelOnlyAdmission) ObserveResult(string, string, int) {}

func (labelOnlyAdmission) ObserveTransportFailure(string, string, error) {}
