package api

import (
	"decentralized-api/apiconfig"
	"fmt"
	"log/slog"
	"net/http"
)

// v1/admin/unit-of-compute-bid
func WrapUnitOfComputeBid(configManager *apiconfig.ConfigManager) func(w http.ResponseWriter, request *http.Request) {
	return func(w http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodPost:
			postUnitOfComputeBid(configManager)
		case http.MethodGet:
			getUnitOfComputeBid(configManager)
		default:
			slog.Error("Invalid request method", "method", request.Method, "path", request.URL.Path)
			msg := fmt.Sprintf("Invalid request method. method = %s. path = %s", request.Method, request.URL.Path)
			http.Error(w, msg, http.StatusMethodNotAllowed)
		}
	}
}

type UnitOfComputeBidDto struct {
	Bid string `json:"bid"`
}

func postUnitOfComputeBid(configManager *apiconfig.ConfigManager) {
	configManager.GetConfig()
}

func getUnitOfComputeBid(configManager *apiconfig.ConfigManager) {

}
