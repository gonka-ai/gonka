package accounting

const diagnosticRingSize = 256

type diagnosticRing struct {
	entries [diagnosticRingSize]DiagnosticEntry
	next    int
	count   int
}

func (b *Book) SetBlockHeightProvider(provider func() int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.blockHeight = provider
}

func (b *Book) Diagnostics() []DiagnosticEntry {
	b.mu.RLock()
	defer b.mu.RUnlock()
	result := make([]DiagnosticEntry, b.diagnostics.count)
	start := b.diagnostics.next - b.diagnostics.count
	if start < 0 {
		start += diagnosticRingSize
	}
	for i := range result {
		result[i] = b.diagnostics.entries[(start+i)%diagnosticRingSize]
	}
	return result
}

func (b *Book) appendDiagnostic(event Event) {
	var entry DiagnosticEntry
	switch e := event.(type) {
	case EscrowRegistered:
		entry = DiagnosticEntry{Kind: DiagnosticEscrowRegistered, EscrowID: e.Metadata.EscrowID, Phase: string(e.Metadata.Phase)}
	case EscrowPhaseChanged:
		entry = DiagnosticEntry{Kind: DiagnosticPhaseChanged, EscrowID: e.EscrowID, Phase: string(e.Phase)}
	case DiffApplied:
		entry = DiagnosticEntry{Kind: DiagnosticDiffApplied, EscrowID: e.EscrowID, Nonce: e.Nonce}
	case RealSend:
		entry = DiagnosticEntry{Kind: DiagnosticRealSend, EscrowID: e.EscrowID, Nonce: e.Nonce, Phase: string(e.DispatchPhase)}
	case Ghost:
		entry = DiagnosticEntry{Kind: DiagnosticGhost, EscrowID: e.EscrowID, Nonce: e.Nonce, Phase: string(e.DispatchPhase), Reason: string(normalizeNoSendReason(e.Reason))}
	case Winner:
		entry = DiagnosticEntry{Kind: DiagnosticUsage, EscrowID: e.EscrowID, Nonce: e.Nonce, Reason: string(UsageWinner)}
	case Loser:
		entry = DiagnosticEntry{Kind: DiagnosticUsage, EscrowID: e.EscrowID, Nonce: e.Nonce, Reason: string(UsageLoser)}
	case UsageUnknown:
		entry = DiagnosticEntry{Kind: DiagnosticUsage, EscrowID: e.EscrowID, Nonce: e.Nonce, Reason: string(UsageUnknownValue)}
	case TimeoutRequired:
		entry = DiagnosticEntry{Kind: DiagnosticDeadline, EscrowID: e.EscrowID, Nonce: e.Nonce, Phase: string(e.EvaluationPhase), Reason: string(e.Kind)}
	case TimeoutOutcomeRecorded:
		entry = DiagnosticEntry{Kind: DiagnosticTimeoutOutcome, EscrowID: e.EscrowID, Nonce: e.Nonce, Reason: string(e.Outcome)}
	case ProtocolTransition:
		entry = DiagnosticEntry{Kind: DiagnosticProtocol, EscrowID: e.EscrowID, Nonce: e.Nonce, Reason: string(e.Kind)}
	case HostStatsObserved:
		return
	default:
		return
	}
	if b.blockHeight != nil {
		entry.BlockHeight = b.blockHeight()
	}
	b.diagnostics.entries[b.diagnostics.next] = entry
	b.diagnostics.next = (b.diagnostics.next + 1) % diagnosticRingSize
	if b.diagnostics.count < diagnosticRingSize {
		b.diagnostics.count++
	}
}
