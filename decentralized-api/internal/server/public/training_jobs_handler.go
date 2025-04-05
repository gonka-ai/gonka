package public

import (
	"decentralized-api/api/model"
	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/types"
	"net/http"
	"strconv"
)

// TODO add signature verification
func (s *Server) postTrainingJob(ctx echo.Context) error {
	var body model.StartTrainingDto
	if err := ctx.Bind(&body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	var hardwareResources = make([]*inference.TrainingHardwareResources, len(body.HardwareResources))
	for i, hr := range body.HardwareResources {
		hardwareResources[i] = &inference.TrainingHardwareResources{
			Type_: hr.Type,
			Count: hr.Count,
		}
	}

	msg := &inference.MsgCreateTrainingTask{
		HardwareResources: hardwareResources,
		Config: &inference.TrainingConfig{
			Datasets: &inference.TrainingDatasets{
				Train: body.Config.Datasets.Train,
				Test:  body.Config.Datasets.Test,
			},
			NumUocEstimationSteps: body.Config.NumUocEstimationSteps,
		},
	}

	msgResponse, err := s.recorder.CreateTrainingTask(msg)
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusCreated, msgResponse)
}

func (s *Server) getTrainingJob(ctx echo.Context) error {
	idParam := ctx.Param("id")
	uintId, err := strconv.ParseUint(idParam, 10, 64)
	if err != nil {
		return ErrInvalidTrainingJobId
	}

	queryClient := s.recorder.NewInferenceQueryClient()
	task, err := queryClient.TrainingTask(*s.recorder.GetContext(), &types.QueryTrainingTaskRequest{Id: uintId})
	if err != nil {
		return err
	}

	return ctx.JSON(http.StatusOK, task)
}
