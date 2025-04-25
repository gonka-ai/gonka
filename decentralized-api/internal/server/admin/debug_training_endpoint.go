package admin

import (
	"github.com/labstack/echo/v4"
	"net/http"
)

type CreateDummyTrainingTaskDto struct {
}

func (s *Server) postDummyTrainingTask(ctx echo.Context) error {
	var body CreateDummyTrainingTaskDto
	if err := ctx.Bind(&body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	return nil
}
