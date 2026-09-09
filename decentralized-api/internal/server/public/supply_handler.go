package public

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/labstack/echo/v4"
)

const (
	gonkaBaseDenom    = "ngonka"
	gonkaDisplayScale = 9
)

type bankSupplyByDenomResponse struct {
	Amount struct {
		Denom  string `json:"denom"`
		Amount string `json:"amount"`
	} `json:"amount"`
}

func (s *Server) getTotalSupply(c echo.Context) error {
	chainNodeURL := s.configManager.GetChainNodeConfig().Url
	chainRESTURL, err := chainRESTURLFromRPCURL(chainNodeURL)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	client := s.httpClient
	if client == nil {
		client = NewNoRedirectClient(httpClientTimeout)
	}

	supply, err := fetchTotalSupplyGonka(c.Request().Context(), client, chainRESTURL)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadGateway, err.Error())
	}

	return c.String(http.StatusOK, supply)
}

func chainRESTURLFromRPCURL(rawURL string) (string, error) {
	if strings.TrimSpace(rawURL) == "" {
		return "http://localhost:1317", nil
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse chain node url: %w", err)
	}

	parsed.Path = strings.TrimRight(parsed.Path, "/")
	switch {
	case strings.HasSuffix(parsed.Path, "/chain-rpc"):
		parsed.Path = strings.TrimSuffix(parsed.Path, "/chain-rpc") + "/chain-api"
	case parsed.Port() == "26657":
		parsed.Host = net.JoinHostPort(parsed.Hostname(), "1317")
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""

	return strings.TrimRight(parsed.String(), "/"), nil
}

func fetchTotalSupplyGonka(ctx context.Context, client *http.Client, chainRESTURL string) (string, error) {
	endpoint := strings.TrimRight(chainRESTURL, "/") + "/cosmos/bank/v1beta1/supply/by_denom?denom=" + gonkaBaseDenom
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("create total supply request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("query total supply: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read total supply response: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("chain total supply query failed: status=%d", resp.StatusCode)
	}

	var parsed bankSupplyByDenomResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("decode total supply response: %w", err)
	}
	if parsed.Amount.Denom != gonkaBaseDenom {
		return "", fmt.Errorf("unexpected total supply denom %q", parsed.Amount.Denom)
	}

	return formatNgonkaAsGonka(parsed.Amount.Amount)
}

func formatNgonkaAsGonka(amount string) (string, error) {
	ngonka, ok := new(big.Int).SetString(amount, 10)
	if !ok || ngonka.Sign() < 0 {
		return "", fmt.Errorf("invalid ngonka amount %q", amount)
	}

	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(gonkaDisplayScale), nil)
	whole, fraction := new(big.Int).QuoRem(ngonka, scale, new(big.Int))
	return fmt.Sprintf("%s.%0*s", whole.String(), gonkaDisplayScale, fraction.String()), nil
}
