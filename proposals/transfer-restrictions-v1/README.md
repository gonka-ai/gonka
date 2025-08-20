# Transfer Restrictions V1 Proposal

This directory contains the proposal for implementing native coin transfer restrictions during the network's initial bootstrapping phase.

## Overview

The proposal introduces controlled native coin transfer restrictions for the first 1,555,000 blocks (~90 days) while preserving essential network operations including gas payments and inference fees.

## Documents

- **[transfer-restrictions.md](./transfer-restrictions.md)** - Main proposal document with complete implementation details

## Key Features

- **SendRestriction Implementation**: Uses Cosmos SDK's bank module restrictions
- **Essential Service Exemptions**: Gas fees and inference payments remain functional
- **Time-Based Activation**: Automatic lifting after block height 1,555,000
- **Governance Override**: Emergency mechanism for critical operations
- **Bootstrap Stability**: Prevents speculative trading during early network development

## Implementation Status

- [ ] Proposal review and approval
- [ ] SendRestriction function implementation
- [ ] Bank module integration
- [ ] Testing and validation
- [ ] Network deployment via governance upgrade

## Related Proposals

- [Tokenomics V2](../tokenomics-v2/) - Bitcoin-style reward system
- [Simple Schedule V1](../simple-schedule-v1/) - Multi-model and GPU uptime system

## Timeline

**Estimated Duration**: 90 days from network genesis
**End Block**: 1,555,000
**Automatic Lifting**: No governance action required
