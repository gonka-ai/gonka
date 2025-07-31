package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/suite"

	testkeeper "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/collateral/keeper"
	"github.com/productscience/inference/x/collateral/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

type KeeperTestSuite struct {
	suite.Suite

	ctx        sdk.Context
	k          keeper.Keeper
	bankKeeper *testkeeper.MockBookkeepingBankKeeper
	msgServer  types.MsgServer
}

func (s *KeeperTestSuite) SetupTest() {
	k, ctx, mocks := testkeeper.CollateralKeeperReturningMocks(s.T())

	s.ctx = ctx
	s.k = k
	s.bankKeeper = mocks.BankKeeper
	s.msgServer = keeper.NewMsgServerImpl(s.k)
}

func TestKeeperTestSuite(t *testing.T) {
	suite.Run(t, new(KeeperTestSuite))
}
