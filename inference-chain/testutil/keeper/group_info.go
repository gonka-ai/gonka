package keeper

import (
	"context"
	"strconv"

	"github.com/cosmos/cosmos-sdk/x/group"
	"go.uber.org/mock/gomock"
)

// ExpectLiveGroupInfo answers GroupInfo with the sum of the mocked members, as x/group does.
func ExpectLiveGroupInfo(gk *MockGroupMessageKeeper) {
	gk.EXPECT().GroupInfo(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, req *group.QueryGroupInfoRequest) (*group.QueryGroupInfoResponse, error) {
			resp, err := gk.GroupMembers(ctx, &group.QueryGroupMembersRequest{GroupId: req.GroupId})
			if err != nil {
				return nil, err
			}
			var total int64
			for _, m := range resp.Members {
				w, _ := strconv.ParseInt(m.Member.Weight, 10, 64)
				total += w
			}
			return &group.QueryGroupInfoResponse{Info: &group.GroupInfo{Id: req.GroupId, TotalWeight: strconv.FormatInt(total, 10)}}, nil
		},
	).AnyTimes()
}
