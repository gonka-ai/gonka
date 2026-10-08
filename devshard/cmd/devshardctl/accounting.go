package main

import (
	"devshard/accounting"
	"devshard/user"
)

func attachAccounting(recorder *accounting.Recorder, rt *devshardRuntime) {
	if recorder == nil || rt == nil || rt.session == nil || rt.proxy == nil || rt.proxy.sm == nil {
		return
	}
	recorder.Attach(accounting.RuntimeMetadata{
		EscrowID:      rt.id,
		CreationEpoch: rt.creationEpoch,
		Model:         rt.model,
		TimeoutBuffer: user.TimeoutBuffer,
	}, rt.session, rt.proxy.sm)
	if rt.proxy.redundancy != nil {
		rt.proxy.redundancy.accounting = recorder
	}
	rt.accountingRetire = func() { recorder.Retire(rt.id) }
}
