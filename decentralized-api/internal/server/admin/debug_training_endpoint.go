package admin

import (
	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/api/inference/inference"
	"net/http"
	"strconv"
)

type CreateDummyTrainingTaskDto struct {
	TaskId   uint64 `json:"task_id"`
	NumNodes int32  `json:"num_nodes"`
}

func (s *Server) postDummyTrainingTask(ctx echo.Context) error {
	var body CreateDummyTrainingTaskDto
	if err := ctx.Bind(&body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	assignees := make([]*inference.TrainingTaskAssignee, body.NumNodes)
	for i := 0; i < int(body.NumNodes); i++ {
		assignees[i] = &inference.TrainingTaskAssignee{
			Participant: "participant" + strconv.Itoa(i),
			NodeIds:     []string{strconv.Itoa(i)},
		}
	}

	msg := &inference.MsgCreateDummyTrainingTask{
		Creator: s.recorder.GetAccountAddress(),
		Task: &inference.TrainingTask{
			Id:          body.TaskId,
			RequestedBy: s.recorder.GetAccountAddress(),
			Assignees:   assignees,
		},
	}
	dst := &inference.MsgCreateDummyTrainingTaskResponse{}
	err := s.recorder.SendTransactionSyncNoRetry(msg, dst)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err)
	}
	return ctx.JSON(http.StatusOK, dst)
}
