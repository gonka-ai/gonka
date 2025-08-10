package admin

import (
	"decentralized-api/cosmosclient"
	"decentralized-api/logging"
	"fmt"
	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/types"
	"net/http"
)

func (s *Server) registerModel(ctx echo.Context) error {
	var body RegisterModelDto
	if err := ctx.Bind(&body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	authority := cosmosclient.GetProposalMsgSigner()
	logging.Info("RegisterModel", types.Inferences, "authority", authority)
	msg := &inference.MsgRegisterModel{
		Authority:              authority,
		ProposedBy:             s.recorder.GetAccountAddress(),
		Id:                     body.Id,
		UnitsOfComputePerToken: body.UnitsOfComputePerToken,
	}

	proposalData := &cosmosclient.ProposalData{
		Metadata:  "Created via decentralized-api",
		Title:     fmt.Sprintf("%s model proposal", body.Id),
		Summary:   fmt.Sprintf("This proposal suggests to serve a model %s and estimates it will take %d units of compute per token", body.Id, body.UnitsOfComputePerToken),
		Expedited: false,
	}

	// TODO: make it a function of cosmosClient interface?
	err := cosmosclient.SubmitProposal(s.recorder, msg, proposalData)
	if err != nil {
		logging.Error("SubmitProposal failed", types.Inferences, "err", err)
		return err
	}
	return ctx.NoContent(http.StatusOK)
}
