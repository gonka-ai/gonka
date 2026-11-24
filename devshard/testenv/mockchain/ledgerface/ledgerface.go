// Package ledgerface serves read-only mock-chain views of committed txs and
// escrows for load-test invariants. It lives outside adminface because
// txexec imports rpcface, which imports adminface.
package ledgerface

import (
	"encoding/hex"
	"net/http"

	"devshard/testenv/mockchain/store"
	"devshard/testenv/mockchain/txexec"
	"devshard/testenv/mockchain/txledger"

	"github.com/labstack/echo/v4"
)

// TxView is one committed tx from GET /testenv/txs, decoded from the stored
// TxRaw bytes. Error is set when the bytes no longer decode.
type TxView struct {
	Hash   string         `json:"hash"`
	Seq    uint64         `json:"seq"`
	Type   string         `json:"type"`
	Create *CreateTxView  `json:"create,omitempty"`
	Settle *SettleTxView  `json:"settle,omitempty"`
	Events []txexec.Event `json:"events,omitempty"`
	Error  string         `json:"error,omitempty"`
}

// CreateTxView mirrors MsgCreateDevshardEscrow.
type CreateTxView struct {
	Creator string `json:"creator"`
	Amount  uint64 `json:"amount"`
	ModelID string `json:"model_id"`
}

// SettleTxView mirrors MsgSettleDevshardEscrow; byte fields are hex.
type SettleTxView struct {
	Settler                     string               `json:"settler"`
	EscrowID                    uint64               `json:"escrow_id"`
	Nonce                       uint64               `json:"nonce"`
	StateRoot                   string               `json:"state_root"`
	StateRootAndProtocolVersion string               `json:"state_root_and_protocol_version"`
	RestHash                    string               `json:"rest_hash"`
	Fees                        uint64               `json:"fees"`
	HostStats                   []SettleHostStatView `json:"host_stats"`
	SignatureSlots              []uint32             `json:"signature_slots"`
}

// SettleHostStatView is one slot of a settle tx's host_stats.
type SettleHostStatView struct {
	SlotID               uint32 `json:"slot_id"`
	Missed               uint32 `json:"missed"`
	Invalid              uint32 `json:"invalid"`
	Cost                 uint64 `json:"cost"`
	RequiredValidations  uint32 `json:"required_validations"`
	CompletedValidations uint32 `json:"completed_validations"`
}

// EscrowView is one row of GET /testenv/escrows.
type EscrowView struct {
	ID                uint64 `json:"id"`
	Creator           string `json:"creator"`
	ModelID           string `json:"model_id"`
	Amount            uint64 `json:"amount"`
	EpochIndex        uint64 `json:"epoch_index"`
	Settled           bool   `json:"settled"`
	CreateDevshardFee uint64 `json:"create_devshard_fee"`
	FeePerNonce       uint64 `json:"fee_per_nonce"`
}

// Mount registers GET /testenv/txs[?type=settle|create] and GET /testenv/escrows.
func Mount(g *echo.Group, st *store.Store, ledger *txledger.Ledger) {
	if g == nil || st == nil || ledger == nil {
		panic("ledgerface: nil group, store or ledger")
	}
	g.GET("/testenv/txs", handleTxs(ledger))
	g.GET("/testenv/escrows", handleEscrows(st))
}

func handleTxs(ledger *txledger.Ledger) echo.HandlerFunc {
	return func(c echo.Context) error {
		filter := c.QueryParam("type")
		if filter != "" && filter != "settle" && filter != "create" {
			return echo.NewHTTPError(http.StatusBadRequest, "type must be settle or create")
		}
		views := []TxView{}
		for _, tx := range ledger.List() {
			view := decodeTxView(tx)
			if filter != "" && view.Type != filter {
				continue
			}
			views = append(views, view)
		}
		return c.JSON(http.StatusOK, map[string]any{"txs": views})
	}
}

func decodeTxView(tx txledger.StoredTx) TxView {
	view := TxView{Hash: tx.Hash, Seq: tx.Seq, Events: tx.Events}
	msgs, err := txexec.DecodeTxMessages(tx.Raw)
	if err != nil {
		view.Type = "unknown"
		view.Error = err.Error()
		return view
	}
	// ExecMessages admits exactly one message per tx, so the ledger holds one.
	msg := msgs[0]
	switch {
	case msg.Create != nil:
		view.Type = "create"
		view.Create = &CreateTxView{Creator: msg.Create.GetCreator(), Amount: msg.Create.GetAmount(), ModelID: msg.Create.GetModelId()}
	case msg.Settle != nil:
		view.Type = "settle"
		settle := msg.Settle
		out := &SettleTxView{
			Settler:                     settle.GetSettler(),
			EscrowID:                    settle.GetEscrowId(),
			Nonce:                       settle.GetNonce(),
			StateRoot:                   hex.EncodeToString(settle.GetStateRoot()),
			StateRootAndProtocolVersion: settle.GetStateRootAndProtocolVersion(),
			RestHash:                    hex.EncodeToString(settle.GetRestHash()),
			Fees:                        settle.GetFees(),
			HostStats:                   []SettleHostStatView{},
			SignatureSlots:              []uint32{},
		}
		for _, stat := range settle.GetHostStats() {
			if stat == nil {
				continue
			}
			out.HostStats = append(out.HostStats, SettleHostStatView{
				SlotID: stat.SlotId, Missed: stat.Missed, Invalid: stat.Invalid, Cost: stat.Cost,
				RequiredValidations: stat.RequiredValidations, CompletedValidations: stat.CompletedValidations,
			})
		}
		for _, sig := range settle.GetSignatures() {
			if sig != nil {
				out.SignatureSlots = append(out.SignatureSlots, sig.SlotId)
			}
		}
		view.Settle = out
	}
	return view
}

func handleEscrows(st *store.Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		escrows := st.ListEscrows()
		views := make([]EscrowView, 0, len(escrows))
		for _, e := range escrows {
			views = append(views, EscrowView{
				ID: e.Id, Creator: e.Creator, ModelID: e.ModelId, Amount: e.Amount, EpochIndex: e.EpochIndex,
				Settled: e.Settled, CreateDevshardFee: e.CreateDevshardFee, FeePerNonce: e.FeePerNonce,
			})
		}
		return c.JSON(http.StatusOK, map[string]any{"escrows": views})
	}
}
