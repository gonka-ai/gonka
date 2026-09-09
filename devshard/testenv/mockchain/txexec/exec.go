package txexec

import (
	"fmt"
	"strconv"
	"strings"

	inferencetypes "github.com/productscience/inference/x/inference/types"

	"devshard/testenv/mockchain/rpcface"
	"devshard/testenv/mockchain/store"
)

// Event is a Cosmos-style event emitted by mock-chain tx execution.
type Event struct {
	Type       string
	Attributes []Attribute
}

// Attribute is one key/value on an Event.
type Attribute struct {
	Key   string
	Value string
}

// Result is the outcome of executing one signed tx.
type Result struct {
	Events   []Event
	Signer   string
	EscrowID uint64
}

// ExecMessages runs supported inference messages against the mock chain store.
func ExecMessages(st *store.Store, rpc *rpcface.Service, msgs []DecodedMsg) (Result, error) {
	if len(msgs) != 1 {
		return Result{}, fmt.Errorf("mock-chain accepts exactly one message per tx, got %d", len(msgs))
	}
	msg := msgs[0]
	switch {
	case msg.Create != nil:
		return execCreate(st, rpc, msg.Create)
	case msg.Settle != nil:
		return execSettle(st, rpc, msg.Settle)
	default:
		return Result{}, fmt.Errorf("empty decoded message")
	}
}

func execCreate(st *store.Store, rpc *rpcface.Service, msg *inferencetypes.MsgCreateDevshardEscrow) (Result, error) {
	creator := strings.TrimSpace(msg.GetCreator())
	if creator == "" {
		return Result{}, fmt.Errorf("creator is required")
	}
	if msg.GetAmount() == 0 {
		return Result{}, fmt.Errorf("amount is required")
	}
	modelID := strings.TrimSpace(msg.GetModelId())
	if modelID == "" {
		return Result{}, fmt.Errorf("model_id is required")
	}

	id := st.AllocateEscrowID()
	epoch := st.GetEpoch()
	escrow := buildEscrowFromTemplate(st, id, creator, msg.GetAmount(), modelID, epoch.Index)
	st.PutEscrow(escrow)
	if err := rpc.PublishEscrowCreated(id); err != nil {
		return Result{}, err
	}
	st.IncrementSequence(creator)

	events := []Event{{
		Type: "devshard_escrow_created",
		Attributes: []Attribute{
			{Key: "escrow_id", Value: strconv.FormatUint(id, 10)},
			{Key: "creator", Value: creator},
			{Key: "amount", Value: strconv.FormatUint(msg.GetAmount(), 10)},
			{Key: "epoch_index", Value: strconv.FormatUint(epoch.Index, 10)},
			{Key: "model_id", Value: modelID},
		},
	}}
	return Result{Events: events, Signer: creator, EscrowID: id}, nil
}

func execSettle(st *store.Store, rpc *rpcface.Service, msg *inferencetypes.MsgSettleDevshardEscrow) (Result, error) {
	settler := strings.TrimSpace(msg.GetSettler())
	if settler == "" {
		return Result{}, fmt.Errorf("settler is required")
	}
	id := msg.GetEscrowId()
	if id == 0 {
		return Result{}, fmt.Errorf("escrow_id is required")
	}
	if !st.MarkEscrowSettled(id) {
		return Result{}, fmt.Errorf("escrow %d not found", id)
	}
	fees := msg.GetFees()
	totalPayout := fees
	remainder := uint64(0)
	if err := rpc.PublishEscrowSettled(id, settler, totalPayout, fees, remainder); err != nil {
		return Result{}, err
	}
	st.IncrementSequence(settler)

	events := []Event{{
		Type: "devshard_escrow_settled",
		Attributes: []Attribute{
			{Key: "escrow_id", Value: strconv.FormatUint(id, 10)},
			{Key: "settler", Value: settler},
			{Key: "total_payout", Value: strconv.FormatUint(totalPayout, 10)},
			{Key: "fees", Value: strconv.FormatUint(fees, 10)},
			{Key: "remainder", Value: strconv.FormatUint(remainder, 10)},
		},
	}}
	return Result{Events: events, Signer: settler, EscrowID: id}, nil
}

func buildEscrowFromTemplate(st *store.Store, id uint64, creator string, amount uint64, modelID string, epochIndex uint64) *inferencetypes.DevshardEscrow {
	tmpl := st.TemplateEscrow()
	escrow := &inferencetypes.DevshardEscrow{
		Id:         id,
		Creator:    creator,
		Amount:     amount,
		ModelId:    modelID,
		EpochIndex: epochIndex,
		Slots:      []string{"http://versiond-router:8080/devshard/v1"},
		AppHash:    "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20",
	}
	if tmpl != nil {
		if len(tmpl.Slots) > 0 {
			escrow.Slots = append([]string(nil), tmpl.Slots...)
		}
		if tmpl.AppHash != "" {
			escrow.AppHash = tmpl.AppHash
		}
		escrow.TokenPrice = tmpl.TokenPrice
		escrow.CreateDevshardFee = tmpl.CreateDevshardFee
		escrow.FeePerNonce = tmpl.FeePerNonce
		escrow.InferenceSealGraceNonces = tmpl.InferenceSealGraceNonces
		escrow.InferenceSealGraceSeconds = tmpl.InferenceSealGraceSeconds
		escrow.AutoSealEveryNNonces = tmpl.AutoSealEveryNNonces
		escrow.ValidationRate = tmpl.ValidationRate
		escrow.VoteThresholdFactor = tmpl.VoteThresholdFactor
		escrow.RefusalTimeout = tmpl.RefusalTimeout
		escrow.ExecutionTimeout = tmpl.ExecutionTimeout
	}
	// Live /testenv/params patches update DevshardEscrowParams; prefer those for
	// new escrows so validation_rate=10000 (etc.) applies after seed template load.
	if p := st.GetParams().DevshardEscrowParams; p != nil {
		if p.ValidationRate != 0 {
			escrow.ValidationRate = p.ValidationRate
		}
		if p.VoteThresholdFactor != 0 {
			escrow.VoteThresholdFactor = p.VoteThresholdFactor
		}
		if p.RefusalTimeout > 0 {
			escrow.RefusalTimeout = p.RefusalTimeout
		}
		if p.ExecutionTimeout > 0 {
			escrow.ExecutionTimeout = p.ExecutionTimeout
		}
	}
	return escrow
}
