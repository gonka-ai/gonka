package types

import (
	"net/url"
	"strconv"
	"strings"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var (
	_ sdk.Msg = &MsgAutokickTrainshardNode{}
	_ sdk.Msg = &MsgRefreshTrainingNodeOptIn{}
)

const (
	pinnedDigestSeparator   = "@sha256:"
	pinnedDigestLen         = 64
	maxPinnedImageLen       = 512
	MaxRefreshOptInNodes    = 256
	maxTrainingEndpointLen  = 256
	maxAutokickReasonLen    = 256
	maxAutokickRequestIdLen = 128
)

func ValidatePinnedImage(image string) error {
	repository, digest, found := strings.Cut(image, pinnedDigestSeparator)
	if !found || repository == "" || len(digest) != pinnedDigestLen || len(image) > maxPinnedImageLen {
		return ErrTrainshardBaseImageInvalid.Wrap(image)
	}
	for _, c := range digest {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return ErrTrainshardBaseImageInvalid.Wrap(image)
		}
	}
	return nil
}

func (msg *MsgAutokickTrainshardNode) ValidateBasic() error {
	if _, err := sdk.AccAddressFromBech32(msg.Creator); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	if _, err := sdk.AccAddressFromBech32(msg.Participant); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid participant address (%s)", err)
	}
	if msg.NodeId == "" {
		return ErrPocNodeIdEmpty
	}
	if msg.RequestId == "" || len(msg.RequestId) > maxAutokickRequestIdLen {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "request_id length must be in (0, %d]", maxAutokickRequestIdLen)
	}
	if len(msg.Reason) > maxAutokickReasonLen {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "reason length must not exceed %d", maxAutokickReasonLen)
	}
	return nil
}

func (msg *MsgRefreshTrainingNodeOptIn) ValidateBasic() error {
	if _, err := sdk.AccAddressFromBech32(msg.Creator); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	if len(msg.NodeIds) == 0 || len(msg.NodeIds) > MaxRefreshOptInNodes {
		return ErrTrainshardOptInRequest.Wrapf("node_ids count must be in (0, %d]", MaxRefreshOptInNodes)
	}
	seen := make(map[string]bool, len(msg.NodeIds))
	for _, nodeId := range msg.NodeIds {
		if nodeId == "" {
			return ErrPocNodeIdEmpty
		}
		if seen[nodeId] {
			return ErrTrainshardOptInRequest.Wrapf("duplicate node_id %s", nodeId)
		}
		seen[nodeId] = true
	}
	if msg.Endpoint != "" {
		return ValidateTrainingEndpoint(msg.Endpoint)
	}
	return nil
}

// ValidateTrainingEndpoint accepts an absolute http(s) base a coordinator can append a path to:
// a host, optionally a port and a path, and nothing that would survive into a signed request
// unread, such as a query, a fragment or credentials
func ValidateTrainingEndpoint(endpoint string) error {
	if len(endpoint) > maxTrainingEndpointLen {
		return ErrTrainshardOptInRequest.Wrapf("endpoint longer than %d", maxTrainingEndpointLen)
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return ErrTrainshardOptInRequest.Wrapf("endpoint %q: %v", endpoint, err)
	}
	switch {
	case parsed.Scheme != "http" && parsed.Scheme != "https":
		return ErrTrainshardOptInRequest.Wrapf("endpoint %q: scheme must be http or https", endpoint)
	case parsed.Hostname() == "":
		return ErrTrainshardOptInRequest.Wrapf("endpoint %q: host is empty", endpoint)
	case parsed.User != nil || parsed.Opaque != "" || strings.ContainsAny(endpoint, "?#"):
		return ErrTrainshardOptInRequest.Wrapf("endpoint %q: only scheme, host, port and path are allowed", endpoint)
	case strings.HasSuffix(parsed.Path, "/"):
		return ErrTrainshardOptInRequest.Wrapf("endpoint %q: no trailing slash", endpoint)
	case strings.HasSuffix(parsed.Host, ":") || !validPort(parsed.Port()):
		return ErrTrainshardOptInRequest.Wrapf("endpoint %q: port must be in 1..65535", endpoint)
	}
	return nil
}

func validPort(port string) bool {
	if port == "" {
		return true
	}
	n, err := strconv.Atoi(port)
	return err == nil && n >= 1 && n <= 65535
}
