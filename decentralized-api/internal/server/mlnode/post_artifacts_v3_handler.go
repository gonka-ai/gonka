package mlnode

import (
	"decentralized-api/mlnodeclient"
	"fmt"
	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
)

func (s *Server) postGeneratedArtifactsV3(ctx echo.Context) error {
	return s.postGeneratedArtifacts(ctx, types.PocScheme_POC_SCHEME_DECODE)
}

func (s *Server) postValidatedArtifactsV3(ctx echo.Context) error {
	return s.postValidatedArtifacts(ctx, types.PocScheme_POC_SCHEME_DECODE)
}

func decodeArtifactVector(artifact mlnodeclient.ArtifactV2, steps int64) ([]byte, error) {
	if int64(len(artifact.KPointsSteps)) != steps+1 {
		return nil, fmt.Errorf("invalid decode trajectory length: expected %d", steps+1)
	}
	return mlnodeclient.KStepsToBytes(artifact.KPointsSteps)
}
