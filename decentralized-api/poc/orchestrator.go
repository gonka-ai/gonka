package poc

import (
	"decentralized-api/broker"
	"decentralized-api/chainphase"
	cosmosclient "decentralized-api/cosmosclient"
	"decentralized-api/logging"

	"github.com/productscience/inference/x/inference/types"
)

type Orchestrator interface {
	ValidateReceivedArtifacts(pocStageStartBlockHeight int64, pocStartBlockHash string)
}

type orchestratorImpl struct {
	validatorV1  *OnChainValidator
	validatorV2  *OffChainValidator
	phaseTracker *chainphase.ChainPhaseTracker
}

func NewOrchestrator(
	pubKey string,
	validatorAddress string,
	nodeBroker *broker.Broker,
	callbackUrl string,
	chainNodeUrl string,
	cosmosClient cosmosclient.CosmosMessageClient,
	phaseTracker *chainphase.ChainPhaseTracker,
) Orchestrator {
	config := DefaultValidationConfig()

	validatorV2 := NewOffChainValidator(
		cosmosClient,
		nodeBroker,
		phaseTracker,
		callbackUrl,
		pubKey,
		validatorAddress,
		chainNodeUrl,
		config,
	)

	validatorV1 := NewOnChainValidator(
		cosmosClient,
		nodeBroker,
		phaseTracker,
		callbackUrl,
		pubKey,
		validatorAddress,
		chainNodeUrl,
		config,
	)

	return &orchestratorImpl{
		validatorV1:  validatorV1,
		validatorV2:  validatorV2,
		phaseTracker: phaseTracker,
	}
}

func (o *orchestratorImpl) shouldUseV2() bool {
	if o.phaseTracker == nil {
		return true
	}
	return ShouldUseV2FromEpochState(o.phaseTracker.GetCurrentEpochState())
}

func (o *orchestratorImpl) ValidateReceivedArtifacts(pocStageStartBlockHeight int64, pocStartBlockHash string) {
	if o.shouldUseV2() {
		logging.Info("Orchestrator: delegating to V2 off-chain validator", types.PoC,
			"pocStageStartBlockHeight", pocStageStartBlockHeight,
			"pocStartBlockHash", pocStartBlockHash)
		o.validatorV2.ValidateAll(pocStageStartBlockHeight, pocStartBlockHash)
		return
	}

	logging.Info("Orchestrator: delegating to V1 on-chain validator", types.PoC,
		"pocStageStartBlockHeight", pocStageStartBlockHeight)
	o.validatorV1.ValidateAll(pocStageStartBlockHeight, pocStartBlockHash)
}
