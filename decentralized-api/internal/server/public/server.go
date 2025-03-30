package public

import (
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/cosmosclient"
	"decentralized-api/internal/validation"
	"github.com/labstack/echo/v4"
)

type Server struct {
	e                  *echo.Echo
	nodeBroker         *broker.Broker
	configManager      *apiconfig.ConfigManager
	inferenceValidator *validation.InferenceValidator
	recorder           cosmosclient.CosmosMessageClient
}

func NewServer(
	nodeBroker *broker.Broker,
	configManager *apiconfig.ConfigManager,
	inferenceValidator *validation.InferenceValidator,
	recorder cosmosclient.CosmosMessageClient) *Server {
	e := echo.New()
	s := &Server{
		e:                  e,
		nodeBroker:         nodeBroker,
		configManager:      configManager,
		inferenceValidator: inferenceValidator,
		recorder:           recorder,
	}

	g := e.Group("/public/v1/")
	g.POST("chat/completions", s.PostChat)
	g.GET("chat/completions/:id", s.GetChatById)
	g.GET("pricing", s.GetPricing)
	g.GET("models", s.GetModels)

	return s
}
