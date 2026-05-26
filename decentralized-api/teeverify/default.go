package teeverify

func NewDefaultRegistry() *Registry {
	r := NewRegistry()
	r.Register(IntelTDXLiteVerifier{})
	return r
}
