package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	rpchttp "github.com/tendermint/tendermint/rpc/client/http"
	rpctypes "github.com/tendermint/tendermint/rpc/core/types"
	e2e "github.com/tendermint/tendermint/test/e2e/pkg"
	"github.com/tendermint/tendermint/types"
)

// waitForHeight waits for the network to reach a certain height (or above),
// returning the highest height seen. Errors if the network is not making
// progress at all.
func waitForHeight(testnet *e2e.Testnet, height int64) (*types.Block, *types.BlockID, error) {
	var (
		err          error
		maxResult    *rpctypes.ResultBlock
		clients      = map[string]*rpchttp.HTTP{}
		lastIncrease = time.Now()
	)
	defer func() {
		for _, c := range clients {
			c.Close()
		}
	}()

	for {
		for _, node := range testnet.Nodes {
			if node.Mode == e2e.ModeSeed {
				continue
			}
			client, ok := clients[node.Name]
			if !ok {
				client, err = node.Client()
				if err != nil {
					continue
				}
				clients[node.Name] = client
			}

			ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
			defer cancel()
			result, err := client.Block(ctx, nil)
			if err != nil {
				continue
			}
			if result.Block != nil && (maxResult == nil || result.Block.Height >= maxResult.Block.Height) {
				maxResult = result
				lastIncrease = time.Now()
			}
			if maxResult != nil && maxResult.Block.Height >= height {
				return maxResult.Block, &maxResult.BlockID, nil
			}
		}

		if len(clients) == 0 {
			return nil, nil, errors.New("unable to connect to any network nodes")
		}
		if time.Since(lastIncrease) >= 10*time.Second {
			if maxResult == nil {
				return nil, nil, errors.New("chain stalled at unknown height")
			}
			return nil, nil, fmt.Errorf("chain stalled at height %v", maxResult.Block.Height)
		}
		time.Sleep(1 * time.Second)
	}
}

// waitForNode waits for a node to become available and catch up to the given block height.
func waitForNode(node *e2e.Node, height int64, timeout time.Duration) (*rpctypes.ResultStatus, error) {
	client, err := node.Client()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for {
		status, err := client.Status(ctx)
		if err == nil && status.SyncInfo.LatestBlockHeight >= height {
			return status, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
}
