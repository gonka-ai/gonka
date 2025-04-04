package mlnode

import "github.com/labstack/echo/v4"

type Server struct {
	e *echo.Echo
}

func NewServer() *Server {
	e := echo.New()
	return &Server{e}
}
