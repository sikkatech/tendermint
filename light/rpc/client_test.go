package rpc

import (
	"context"
	"testing"
	"time"

	rpcmock "github.com/tendermint/tendermint/rpc/client/mock"
	"github.com/tendermint/tendermint/types"
)

func TestABCIQuery(t *testing.T) {
	c := NewClient(rpcmock.Client{}, lc, KeyPathFn())
	res, err := c.ABCIQuery(context.Background(), "/store/basic/key", types.HexBytes([]byte("test")))

}

type lcStub struct{}

func (lcStub) ChainID() string { return "" }
func (lcStub) VerifyLightBlockAtHeight(ctx context.Context, height int64, now time.Time) (*types.LightBlock, error) {
	return nil, nil
}
func (lcStub) TrustedLightBlock(height int64) *types.LightBlock {
	return nil
}
