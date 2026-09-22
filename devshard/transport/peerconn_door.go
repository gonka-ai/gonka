package transport

import (
	"errors"
	"strings"

	"connectrpc.com/connect"

	"devshard/logging"
	"devshard/transport/rpcpb/rpcpbconnect"
)

func validAttachDoorID(id string) bool {
	id = strings.TrimSpace(id)
	return id != "" && id != HostRPCEscrowID
}

func isDeadDoorError(err error) bool {
	if err == nil {
		return false
	}
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return false
	}
	if ce.Code() != connect.CodeFailedPrecondition {
		return false
	}
	switch ce.Meta().Get(HeaderDevshardError) {
	case DevshardErrorEscrowNotFound, DevshardErrorEscrowSettled:
		return true
	}
	msg := strings.ToLower(ce.Message())
	return strings.Contains(msg, "escrow is not open") || strings.Contains(msg, "escrow settled")
}

func (p *PeerConn) addDoor(id string) {
	if p == nil || !validAttachDoorID(id) {
		return
	}
	p.doorMu.Lock()
	defer p.doorMu.Unlock()
	p.doors[id]++
}

// preferDoor aims the next !liveSession Attach at id (WaitReady's escrow).
// A killed door is not resurrected.
func (p *PeerConn) preferDoor(id string) {
	if p == nil || !validAttachDoorID(id) {
		return
	}
	p.doorMu.Lock()
	defer p.doorMu.Unlock()
	if _, dead := p.deadDoors[id]; dead {
		return
	}
	p.attachDoor = id
}

func (p *PeerConn) dropDoor(id string) {
	if p == nil || !validAttachDoorID(id) {
		return
	}
	p.doorMu.Lock()
	n := p.doors[id] - 1
	if n <= 0 {
		delete(p.doors, id)
	} else {
		p.doors[id] = n
	}
	gone := p.pickDoorLocked() == ""
	p.doorMu.Unlock()
	if gone {
		p.wakeWaiters()
	}
}

func (p *PeerConn) killDoor(id string) {
	if p == nil || !validAttachDoorID(id) {
		return
	}
	p.doorMu.Lock()
	if _, dead := p.deadDoors[id]; dead {
		p.doorMu.Unlock()
		return
	}
	p.deadDoors[id] = struct{}{}
	gone := p.pickDoorLocked() == ""
	p.doorMu.Unlock()
	logging.Warn("peer rpc attach door is gone; trying another escrow",
		"subsystem", "transport",
		"host", p.cfg.HostAddress,
		"door", id,
	)
	if gone {
		p.wakeWaiters()
	}
}

func (p *PeerConn) pickDoor() string {
	if p == nil {
		return ""
	}
	p.doorMu.Lock()
	defer p.doorMu.Unlock()
	return p.pickDoorLocked()
}

func (p *PeerConn) hasAttachDoor() bool {
	return p.pickDoor() != ""
}

func (p *PeerConn) pickDoorLocked() string {
	if p.doorUsableLocked(p.attachDoor) {
		return p.attachDoor
	}
	if p.doorUsableLocked(p.cfg.DoorEscrowID) {
		p.attachDoor = p.cfg.DoorEscrowID
		return p.attachDoor
	}
	for id := range p.doors {
		if p.doorUsableLocked(id) {
			p.attachDoor = id
			return id
		}
	}
	if len(p.doors) == 0 && len(p.deadDoors) == 0 && validAttachDoorID(p.cfg.DoorEscrowID) {
		p.attachDoor = p.cfg.DoorEscrowID
		return p.attachDoor
	}
	p.attachDoor = ""
	return ""
}

func (p *PeerConn) doorUsableLocked(id string) bool {
	if !validAttachDoorID(id) {
		return false
	}
	if _, dead := p.deadDoors[id]; dead {
		return false
	}
	return p.doors[id] > 0
}

func (p *PeerConn) doorAuthClient(escrowID string) rpcpbconnect.PeerAuthServiceClient {
	if p.useGRPC() {
		if escrowID == p.cfg.DoorEscrowID && p.authDoorGRPC != nil {
			return p.authDoorGRPC
		}
		return rpcpbconnect.NewPeerAuthServiceClient(p.http, p.cfg.connectBase(escrowID), maybeGRPC(connectClientOptions(p.cfg.ReadMaxBytes), true)...)
	}
	if escrowID == p.cfg.DoorEscrowID && p.authDoor != nil {
		return p.authDoor
	}
	return rpcpbconnect.NewPeerAuthServiceClient(p.http, p.cfg.connectBase(escrowID), connectClientOptions(p.cfg.ReadMaxBytes)...)
}
