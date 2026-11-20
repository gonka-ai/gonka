package app

import "reflect"

// installWarmKeyGrantHooks selects the writer from the SDK API compiled into
// the binary, never from node configuration. All validators running the same
// release therefore use the same writer.
//
// SDK #23's named GrantHooks interface does not exist in the older pinned SDK.
// Reflection is confined to startup so both SDKs compile without referring to
// that type. A present but incompatible API is an initialization error, not a
// reason to silently fall back. The callback itself remains statically typed.
// TODO: replace this compatibility adapter with a direct SetGrantHooks call
// and remove the post-handler fallback once the minimum SDK includes #23.
func installWarmKeyGrantHooks(keeper, hooks any) bool {
	setter := reflect.ValueOf(keeper).MethodByName("SetGrantHooks")
	if !setter.IsValid() {
		return false
	}
	typ := setter.Type()
	value := reflect.ValueOf(hooks)
	if typ.NumIn() != 1 || typ.NumOut() != 0 || typ.In(0).Kind() != reflect.Interface || !value.IsValid() || !value.Type().Implements(typ.In(0)) {
		//nolint:forbidigo // Startup-only SDK API check, before InitChain or consensus execution.
		panic("authz: incompatible SetGrantHooks API; update warm-key hook registration")
	}
	setter.Call([]reflect.Value{value})
	return true
}
