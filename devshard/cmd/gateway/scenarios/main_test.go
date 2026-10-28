package scenarios

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain ignores the goroutines that live for the process: database/sql's cleaner, the sqlite finalizer and the unclosable chain client.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreTopFunction("database/sql.(*DB).connectionOpener"),
		goleak.IgnoreTopFunction("modernc.org/sqlite.(*conn).interruptOnDone.func1"),
		goleak.IgnoreTopFunction("github.com/desertbit/timer.timerRoutine"),
		goleak.IgnoreTopFunction("google.golang.org/grpc/internal/grpcsync.(*CallbackSerializer).run"),
		goleak.IgnoreTopFunction("google.golang.org/grpc/internal/resolver/dns.(*dnsResolver).watcher"),
		goleak.IgnoreTopFunction("google.golang.org/grpc.(*addrConn).resetTransportAndUnlock"),
		goleak.IgnoreAnyFunction("github.com/godbus/dbus.(*Conn).inWorker"),
	)
}
