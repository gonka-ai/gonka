package process

import (
	"context"
	"log/slog"
)

type operationKind string

const operationReconcile operationKind = "reconcile"

type controlOperation struct {
	kind   operationKind
	cancel context.CancelFunc
}

func (m *Manager) beginOperation(
	parent context.Context,
	kind operationKind,
) (context.Context, uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.hostDraining {
		return nil, 0, ErrHostDraining
	}
	ctx, cancel := context.WithCancel(parent)
	m.nextOperationID++
	id := m.nextOperationID
	m.operations[id] = controlOperation{kind: kind, cancel: cancel}
	return ctx, id, nil
}

func (m *Manager) endOperation(id uint64) {
	m.mu.Lock()
	operation, ok := m.operations[id]
	if ok {
		delete(m.operations, id)
	}
	m.mu.Unlock()
	if ok {
		operation.cancel()
	}
}

func (m *Manager) cancelOperations() {
	m.mu.Lock()
	type registeredOperation struct {
		id uint64
		controlOperation
	}
	operations := make([]registeredOperation, 0, len(m.operations))
	for id, operation := range m.operations {
		operations = append(operations, registeredOperation{
			id:               id,
			controlOperation: operation,
		})
	}
	m.mu.Unlock()
	for _, operation := range operations {
		slog.Info(
			"canceling process manager operation",
			"operation_id", operation.id,
			"kind", operation.kind,
		)
		operation.cancel()
	}
}
