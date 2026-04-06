package engineinf

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// Transfer handles the transfer path: selects an executor and forwards the request.
// Not yet implemented — returns 501.
type Transfer struct{}

func (t *Transfer) Handle(c echo.Context) error {
	return c.JSON(http.StatusNotImplemented, map[string]string{"error": "transfer path not yet implemented"})
}
