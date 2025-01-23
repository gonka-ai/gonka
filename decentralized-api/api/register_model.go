package api

import "net/http"

type RegisterModelDto struct {
	ModelId string `json:"model_id"`
}

// v1/admin/register-model
func WrapRegisterModel() func(w http.ResponseWriter, request *http.Request) {
	return func(w http.ResponseWriter, request *http.Request) {}
}
