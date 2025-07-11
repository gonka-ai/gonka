# OnNewBlockDispatcher Architecture

## Overview

The OnNewBlockDispatcher provides a more testable and maintainable approach to processing blockchain events. It separates concerns and makes the system more robust by eliminating the need for time-based reconciliation in favor of block-driven reconciliation.

## Key Components

### 1. Data Structures

- **NewBlockInfo**: Parsed block information (height, hash, timestamp)
- **PhaseInfo**: Complete phase state (epoch, phase, block info, PoC parameters, sync status)
- **ReconciliationConfig**: Configures when reconciliation should trigger (block interval + time fallback)

### 2. OnNewBlockDispatcher

The main orchestrator that:
1. Queries network state (sync status, epoch params)
2. Updates phase tracker with pure functions
3. Handles phase transitions and stage events
4. Triggers reconciliation based on block count and time
5. Manages seed generation and money claiming

### 3. Benefits Over Previous Architecture

#### Better Testability
- **Unit Tests**: Can test reconciliation logic without running blockchain
- **Mock Data**: Easy to create fake NewBlockInfo structs for testing
- **Phase Simulation**: Test different phase transitions independently

#### Better Separation of Concerns
- **EventListener**: Only parses events and delegates to dispatcher  
- **Dispatcher**: Handles business logic and orchestration
- **PhaseTracker**: Pure functions for phase management
- **Broker**: Receives commands with all necessary data

#### More Robust Reconciliation
- **Block-Driven**: Reconciliation triggered by actual blockchain progress
- **Self-Contained Commands**: Commands include all phase data at creation time
- **Configurable**: Hybrid approach with block interval + time fallback

## Command Pattern Improvements

All commands now include phase data at creation time instead of accessing shared state:

```go
// Before: Commands accessed phase tracker during execution
StartPocCommand{} // Had to query phase tracker internally

// After: Commands are self-contained
StartPocCommand{
    CurrentEpoch: 5,
    CurrentPhase: chainphase.PhasePoC,
    // ... other fields
}
```

This makes commands:
- **Thread-safe**: No shared state access during execution
- **Testable**: Easy to create with known phase data
- **Predictable**: All data available at command creation time

## Reconciliation Strategy

### Hybrid Approach
- **Primary**: Every N blocks (configurable, default 5)
- **Fallback**: Every N seconds (configurable, default 30s)

### Benefits
- **Responsive**: Reacts immediately to blockchain events
- **Reliable**: Time fallback ensures reconciliation even if blocks are slow
- **Efficient**: No constant polling, only when needed

## Testing Examples

```go
// Test reconciliation logic with fake data
func TestShouldTriggerReconciliation(t *testing.T) {
    dispatcher := &OnNewBlockDispatcher{
        reconciliationConfig: ReconciliationConfig{
            BlockInterval: 5,
            LastBlockHeight: 10,
        },
    }
    
    phaseInfo := &PhaseInfo{BlockHeight: 16} // 6 blocks later
    assert.True(t, dispatcher.shouldTriggerReconciliation(phaseInfo))
}

// Test phase transitions without blockchain
func TestPhaseTransitions(t *testing.T) {
    phaseInfo := &PhaseInfo{
        CurrentPhase: chainphase.PhasePoC,
        PoCParameters: &PoCParams{StartBlockHeight: 1000},
    }
    // Test phase-specific logic...
}
```

## Migration Notes

### EventListener Changes
- Added `OnNewBlockDispatcher` field
- Updated `processEvent` to use dispatcher for new blocks
- Removed direct calls to `poc.ProcessNewBlockEvent`

### Broker Changes
- Disabled time-based `nodeReconciliationWorker`
- Commands now include phase data
- Reconciliation triggered by dispatcher

### PhaseTracker Changes
- Sync status updates moved to dispatcher
- Network queries moved to dispatcher
- Focused on pure state management functions

## Configuration

The reconciliation behavior can be configured:

```go
reconciliationConfig: ReconciliationConfig{
    BlockInterval: 5,                // Every 5 blocks
    TimeInterval:  30 * time.Second, // OR every 30 seconds
}
```

This allows tuning based on network conditions and requirements. 