# Onboarding Clarity Enhancement

## Overview

This proposal addresses critical user experience issues during node onboarding by implementing clear state reporting, proactive testing, and intelligent waiting period management. The current onboarding process leaves users confused about system status, with unclear error messages and no guidance on expected waiting periods or next steps.

## Problem Statement

### Current Onboarding Issues

**Unclear Waiting States**: When users install and start nodes, there is no clear indication that nothing will happen until the next Proof-of-Compute (PoC) cycle begins. Users see their nodes running but receive no feedback about the expected waiting period or what will happen next.

**Confusing Error Messages**: The API node shows "there is no model for ml node" messages even when the participant is not yet active, creating confusion about whether the setup is correct.

**Lack of Proactive Testing**: New MLnodes are not tested before participating in PoC, leading to failures during critical consensus periods that could have been detected earlier.

**Poor Restart Handling**: When API nodes restart, they don't properly check if participants are part of the active participant set, leading to inconsistent behavior.

**Insufficient Status Information**: Users cannot determine:
- How long until the next PoC cycle
- Whether their MLnode is properly configured
- If they can safely turn off servers during waiting periods
- When they should be online for participation

## Proposed Solution

**Better Status Messages:**
- When MLnode registered, the latest and most visible log should be "waiting for PoC"
- Tell users exactly when next PoC starts
- Tell users when they should bring the server online, and when they can safely turn off servers temporarily
- Show: Info message "Waiting for next PoC cycle (starts in 2h 15m) - you can safely turn off the server and restart it 10 minutes before PoC"

**Proactive Testing:**
- When a new MLnode is registered (and there's >1 hour until PoC), automatically test it
- Test: model loading, health check, inference request
- Only show "waiting for PoC" if the test passes
- If test fails, show clear error with specific problem

**Better Restart Handling:**
- When API node restarts, check if participant is actually in active set
- Don't show confusing error messages if participant isn't active yet
- Instead of: Error "there is no model for ml node" 
- Show: Info message "Participant not yet active - model assignment pending (normal for new participants)"

### Enhanced State Management System

The proposal introduces a comprehensive state management system that provides clear feedback at every stage of the onboarding and participation lifecycle.

#### New MLnode States

**Enhanced State Enumeration**:
- `WAITING_FOR_POC` - MLnode is configured and waiting for next PoC cycle
- `TESTING` - MLnode is undergoing pre-PoC validation testing
- `TEST_FAILED` - MLnode failed validation testing

**Timing Guidance Through Messages**:
The same `WAITING_FOR_POC` state provides different user messages based on timing:
- "Waiting for next PoC cycle (starts in 2h 15m) - you can safely turn off the server and restart it 10 minutes before PoC"
- "PoC starting soon (in 8 minutes) - MLnode must be online now"

#### New API Node Participant States

**Participant Status Tracking**:
- `INACTIVE_WAITING` - Participant registered but not yet in active set
- `ACTIVE_PARTICIPATING` - Participant is in active set and participating in current epoch

### Proactive MLnode Testing System

#### Pre-PoC Validation Flow

**Testing Trigger Conditions**:
- New MLnode registered OR configuration changes detected, when more than 1 hour until next PoC
- Manual testing request through admin interface

**Testing Process**:
1. **Model Loading Test**: MLnode switches to "testing" state and performs similar operations as in "inference" state - load configured models and verify successful loading
2. **Health Check**: Perform inference health check to ensure model is functional
3. **Response Validation**: Send test inference request and validate response
4. **Performance Baseline**: Record loading time and response time metrics

**Test Result Actions**:
- **Success**: Switch MLnode to `WAITING_FOR_POC` state
- **Failure**: Switch to `TEST_FAILED` state with detailed error reporting in both MLnode and API node logs

### Intelligent Timing System

#### PoC Schedule Awareness

**Timing Calculations**:
- Calculate time until next PoC cycle using epoch parameters
- Determine safe offline windows (more than 10 minutes until PoC)
- Provide countdown timers for user interfaces
- Alert users when they should be online

**Existing Epoch Structure Integration**:
- Use existing `Epoch` structure with PoC start block height and upcoming epoch information
- Leverage existing `EpochParams` with `PocStageDuration`, `PocValidationDuration`, and other timing parameters
- Utilize current `chainphase.EpochState` and block height tracking for accurate PoC timing calculations

### Enhanced Logging and Status Reporting

#### User-Friendly Status Messages

**Clear State Communications**:
- "Waiting for next PoC cycle (starts in 2h 15m) - you can safely turn off the server and restart it 10 minutes before PoC"
- "Testing MLnode configuration - model loading in progress"
- "MLnode test failed: model 'Qwen/Qwen2.5-7B-Instruct' could not be loaded"
- "PoC starting soon (in 8 minutes) - MLnode must be online now"

**Contextual Error Messages**:
- Suppress "no model for ml node" messages when participant is inactive
- Show clear explanations: "Participant not yet active - model assignment will occur after joining active set"
- Provide actionable guidance: "MLnode will be tested automatically when there is more than 1 hour until next PoC"

#### Enhanced Logging Categories

**Extend Existing Logging System**:
- Enhance existing `types.Nodes` logging with onboarding state transitions
- Add testing-specific logs within existing `types.Nodes` category
- Use existing `types.Participants` for participant status changes
- Integrate timing guidance into existing log categories rather than creating new ones

### Implementation Architecture

#### API Node Enhancements

**New Components for Admin Server** (`decentralized-api/internal/server/admin/`):
- `OnboardingStateManager` - Centralized state tracking for onboarding process
- `MLnodeTestingOrchestrator` - Manages pre-PoC testing workflows  
- `TimingCalculator` - Computes PoC schedules and safe offline windows
- `StatusReporter` - Generates user-friendly status messages

**MLnode Server** (`decentralized-api/internal/server/mlnode/server.go`):
- No changes needed - existing PoC batch endpoints remain the same

#### Integration Points

**Broker Integration**:
- Enhance `NodeState` structure with new onboarding-specific fields
- Modify `RegisterNode` command to trigger testing when appropriate
- Update status query results to include timing information

**Chain Integration**:
- Query active participant status during startup
- Monitor epoch transitions for timing calculations
- Track participant weight changes for status updates

### Conclusion

This proposal addresses critical gaps in the Gonka network's onboarding experience by providing clear state communication, proactive testing, and intelligent timing guidance. The implementation focuses on user experience improvements while maintaining system reliability and security.

The modular architecture allows for incremental deployment and easy maintenance, while the comprehensive testing approach ensures that configuration issues are caught early rather than during critical consensus periods. This results in a more reliable network and significantly improved user experience for node operators.

The enhanced status reporting and timing guidance eliminate confusion about waiting periods and provide clear actionable information, transforming the onboarding process from a frustrating guessing game into a transparent, guided experience.
