package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/bls/types"
)

// FlexibleUint64 handles both string and number JSON inputs
type FlexibleUint64 uint64

func (f *FlexibleUint64) UnmarshalJSON(data []byte) error {
	// Try to unmarshal as a number first
	var num uint64
	if err := json.Unmarshal(data, &num); err == nil {
		*f = FlexibleUint64(num)
		return nil
	}

	// If that fails, try as a string
	var str string
	if err := json.Unmarshal(data, &str); err != nil {
		return fmt.Errorf("current_epoch_id must be a number or string, got %s", data)
	}

	// Convert string to uint64
	num, err := strconv.ParseUint(str, 10, 64)
	if err != nil {
		return fmt.Errorf("current_epoch_id string is not a valid number: %s", str)
	}

	*f = FlexibleUint64(num)
	return nil
}

func (f FlexibleUint64) ToUint64() uint64 {
	return uint64(f)
}

type RequestThresholdSignatureDto struct {
	CurrentEpochId FlexibleUint64 `json:"current_epoch_id"`
	ChainId        []byte         `json:"chain_id"`
	RequestId      []byte         `json:"request_id"`
	Data           [][]byte       `json:"data"`
}

func (s *Server) postRequestThresholdSignature(c echo.Context) error {
	var body RequestThresholdSignatureDto
	if err := c.Bind(&body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	msg := &types.MsgRequestThresholdSignature{
		Creator:        s.recorder.GetAccountAddress(),
		CurrentEpochId: body.CurrentEpochId.ToUint64(),
		ChainId:        body.ChainId,
		RequestId:      body.RequestId,
		Data:           body.Data,
	}

	_, err := s.recorder.SendTransactionAsyncNoRetry(msg)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to send transaction: "+err.Error())
	}

	return c.NoContent(http.StatusOK)
}
