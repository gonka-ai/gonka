package api

import (
	"decentralized-api/api/model"
	"net/http"
)

func WrapPricing() http.HandlerFunc {
	return func(w http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			getPricing(w)
		default:
			http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		}
	}
}

func getPricing(w http.ResponseWriter) {

	var responseBody = &model.PricingDto{
		Price:  0,
		Models: []model.ModelPriceDto{},
	}

	RespondWithJson(w, responseBody)
}
