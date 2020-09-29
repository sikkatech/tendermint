package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math"
	"time"

	e2e "github.com/tendermint/tendermint/test/e2e/pkg"
	"github.com/tendermint/tendermint/types"
)

// Load generates transactions against the network until the given
// context is cancelled.
func Load(ctx context.Context, testnet *e2e.Testnet) error {
	concurrency := 10
	initialTimeout := 1 * time.Minute
	stallTimeout := 15 * time.Second

	chTx := make(chan types.Tx)
	chSuccess := make(chan types.Tx)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Spawn job generator and processors.
	logger.Info("Starting transaction load...")

	go loadGenerate(ctx, chTx)

	for w := 0; w < concurrency; w++ {
		go loadProcess(ctx, testnet, chTx, chSuccess)
	}

	// Monitor successful transactions, and abort on stalls.
	success := 0
	timeout := initialTimeout
	for {
		select {
		case <-chSuccess:
			success++
			timeout = stallTimeout
		case <-time.After(timeout):
			return fmt.Errorf("unable to submit transactions for %v", timeout)
		case <-ctx.Done():
			if success == 0 {
				return errors.New("failed to submit any transactions")
			}
			logger.Info("Ending transaction load...")
			return nil
		}
	}
}

// loadGenerate generates jobs until the context is cancelled
func loadGenerate(ctx context.Context, chTx chan<- types.Tx) {
	for i := 0; i < math.MaxInt64; i++ {
		// We keep generating the same 10000 keys over and over, with different values.
		// This gives a reasonable load without putting too much data in the app.
		id := i % 10000

		bz := make([]byte, 512) // 1kb hex-encoded
		_, err := rand.Read(bz)
		if err != nil {
			panic(fmt.Sprintf("Failed to read random bytes: %v", err))
		}
		tx := types.Tx(fmt.Sprintf("load-%X=%x", id, bz))

		select {
		case chTx <- tx:
			time.Sleep(10 * time.Millisecond)
		case <-ctx.Done():
			close(chTx)
			return
		}
	}
}

// loadProcess processes transactions
func loadProcess(ctx context.Context, testnet *e2e.Testnet, chTx <-chan types.Tx, chSuccess chan<- types.Tx) {
	for tx := range chTx {
		client, err := testnet.RandomNode().Client()
		if err != nil {
			client.Close()
			continue
		}
		_, err = client.BroadcastTxSync(ctx, tx)
		if err != nil {
			client.Close()
			continue
		}
		chSuccess <- tx
		client.Close()
	}
}
