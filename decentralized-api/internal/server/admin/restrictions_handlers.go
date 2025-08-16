package admin

import (
	"decentralized-api/cosmosclient"
	"decentralized-api/logging"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types" // For logging types, adjust if needed
	restrictionstypes "github.com/productscience/inference/x/restrictions/types"
)

// Custom DTO with flexible types for JSON unmarshaling
type EmergencyTransferExemptionDto struct {
	ExemptionId   string         `json:"exemption_id"`
	FromAddress   string         `json:"from_address"`
	ToAddress     string         `json:"to_address"`
	MaxAmount     string         `json:"max_amount"`
	UsageLimit    FlexibleUint64 `json:"usage_limit"`
	ExpiryBlock   FlexibleUint64 `json:"expiry_block"`
	Justification string         `json:"justification"`
}

type ExemptionUsageDto struct {
	ExemptionId    string         `json:"exemption_id"`
	AccountAddress string         `json:"account_address"`
	UsageCount     FlexibleUint64 `json:"usage_count"`
}

type UpdateRestrictionsParamsDto struct {
	RestrictionEndBlock         FlexibleUint64                  `json:"restriction_end_block"`
	EmergencyTransferExemptions []EmergencyTransferExemptionDto `json:"emergency_transfer_exemptions"`
	ExemptionUsageTracking      []ExemptionUsageDto             `json:"exemption_usage_tracking"`
}

func (s *Server) postUpdateRestrictionsParams(c echo.Context) error {
	var body UpdateRestrictionsParamsDto
	if err := c.Bind(&body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	// Convert custom DTOs to protobuf types
	exemptions := make([]restrictionstypes.EmergencyTransferExemption, len(body.EmergencyTransferExemptions))
	for i, dto := range body.EmergencyTransferExemptions {
		exemptions[i] = restrictionstypes.EmergencyTransferExemption{
			ExemptionId:   dto.ExemptionId,
			FromAddress:   dto.FromAddress,
			ToAddress:     dto.ToAddress,
			MaxAmount:     dto.MaxAmount,
			UsageLimit:    dto.UsageLimit.ToUint64(),
			ExpiryBlock:   dto.ExpiryBlock.ToUint64(),
			Justification: dto.Justification,
		}
	}

	usageTracking := make([]restrictionstypes.ExemptionUsage, len(body.ExemptionUsageTracking))
	for i, dto := range body.ExemptionUsageTracking {
		usageTracking[i] = restrictionstypes.ExemptionUsage{
			ExemptionId:    dto.ExemptionId,
			AccountAddress: dto.AccountAddress,
			UsageCount:     dto.UsageCount.ToUint64(),
		}
	}

	msg := &restrictionstypes.MsgUpdateParams{
		Authority: cosmosclient.GetProposalMsgSigner(),
		Params: restrictionstypes.Params{
			RestrictionEndBlock:         body.RestrictionEndBlock.ToUint64(),
			EmergencyTransferExemptions: exemptions,
			ExemptionUsageTracking:      usageTracking,
		},
	}

	proposalData := &cosmosclient.ProposalData{
		Metadata:  "Created via decentralized-api",
		Title:     "Update Restrictions Module Parameters",
		Summary:   "This proposal updates the transfer restrictions parameters including end block and emergency exemptions.",
		Expedited: false,
	}

	err := cosmosclient.SubmitProposal(s.recorder, msg, proposalData)
	if err != nil {
		logging.Error("SubmitProposal failed for restrictions params", types.Inferences, "err", err) // Adjust logging type
		return err
	}

	return c.NoContent(http.StatusOK)
}
