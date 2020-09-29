package main

import (
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/spf13/cobra"
	"github.com/tendermint/tendermint/libs/log"
	e2e "github.com/tendermint/tendermint/test/e2e/pkg"
)

var logger = log.NewTMLogger(log.NewSyncWriter(os.Stdout))

func main() {
	NewCLI().Run()
}

// CLI is the Cobra-based command-line interface.
type CLI struct {
	root    *cobra.Command
	testnet *e2e.Testnet
}

// NewCLI sets up the CLI.
func NewCLI() *CLI {
	cli := &CLI{}
	cli.root = &cobra.Command{
		Use:           "runner",
		Short:         "End-to-end test runner",
		SilenceUsage:  true,
		SilenceErrors: true, // we'll output them ourselves in Run()
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			file, err := cmd.Flags().GetString("file")
			if err != nil {
				return err
			}
			testnet, err := e2e.LoadTestnet(file)
			if err != nil {
				return err
			}

			cli.testnet = testnet
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := Cleanup(cli.testnet); err != nil {
				return err
			}
			if err := Setup(cli.testnet); err != nil {
				return err
			}
			if err := cli.Start(); err != nil {
				return err
			}
			if err := cli.Perturb(); err != nil {
				return err
			}
			if err := Cleanup(cli.testnet); err != nil {
				return err
			}
			return nil
		},
	}

	cli.root.PersistentFlags().StringP("file", "f", "", "Testnet TOML manifest")
	_ = cli.root.MarkPersistentFlagRequired("file")

	cli.root.AddCommand(&cobra.Command{
		Use:   "setup",
		Short: "Generates the testnet directory and configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			return Setup(cli.testnet)
		},
	})

	cli.root.AddCommand(&cobra.Command{
		Use:   "start",
		Short: "Starts the Docker testnet, waiting for nodes to become available",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cli.Start()
		},
	})

	cli.root.AddCommand(&cobra.Command{
		Use:   "perturb",
		Short: "Perturbs the Docker testnet, e.g. by restarting or disconnecting nodes",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cli.Perturb()
		},
	})

	cli.root.AddCommand(&cobra.Command{
		Use:   "stop",
		Short: "Stops the Docker testnet",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger.Info("Stopping testnet")
			return execCompose(cli.testnet.Dir, "down")
		},
	})

	cli.root.AddCommand(&cobra.Command{
		Use:   "logs",
		Short: "Shows the testnet logs",
		RunE: func(cmd *cobra.Command, args []string) error {
			return execComposeVerbose("logs", "--follow")
		},
	})

	cli.root.AddCommand(&cobra.Command{
		Use:   "cleanup",
		Short: "Removes the testnet directory",
		RunE: func(cmd *cobra.Command, args []string) error {
			return Cleanup(cli.testnet)
		},
	})

	return cli
}

// Run runs the CLI.
func (cli *CLI) Run() {
	if err := cli.root.Execute(); err != nil {
		logger.Error(err.Error())
		os.Exit(1)
	}
}

// Start starts the testnet. It waits for all nodes to become available.
func (cli *CLI) Start() error {
	// Sort nodes by starting order
	nodeQueue := cli.testnet.Nodes
	sort.SliceStable(nodeQueue, func(i, j int) bool {
		return nodeQueue[i].StartAt < nodeQueue[j].StartAt
	})

	// We'll use the first non-seed node as our main node for network status
	var mainNode *e2e.Node
	for _, node := range nodeQueue {
		if node.StartAt == 0 && node.Mode != "seed" {
			mainNode = node
			break
		}
	}
	if mainNode == nil {
		return fmt.Errorf("no initial nodes found")
	}

	// Start initial nodes (StartAt: 0)
	logger.Info("Starting initial network nodes...")
	for len(nodeQueue) > 0 && nodeQueue[0].StartAt == 0 {
		node := nodeQueue[0]
		nodeQueue = nodeQueue[1:]
		if mainNode == nil {
			mainNode = node
		}
		if err := execCompose(cli.testnet.Dir, "up", "-d", node.Name); err != nil {
			return err
		}
		if _, err := node.WaitFor(0, 10*time.Second); err != nil {
			return err
		}
		logger.Info(fmt.Sprintf("Node %v up on http://127.0.0.1:%v", node.Name, node.ProxyPort))
	}
	if mainNode == nil {
		return fmt.Errorf("no nodes to start")
	}

	// Wait for initial height
	logger.Info(fmt.Sprintf("Waiting for initial height %v...", cli.testnet.InitialHeight))
	_, err := mainNode.WaitFor(cli.testnet.InitialHeight, 20*time.Second)
	if err != nil {
		return err
	}

	// Fetch the latest block and update any state sync nodes with the trusted height and hash.
	lastBlock, err := mainNode.LastBlock()
	if err != nil {
		return err
	}
	for _, node := range nodeQueue {
		if node.StateSync {
			err = UpdateConfigStateSync(node, lastBlock.Block.Height, lastBlock.BlockID.Hash.Bytes())
			if err != nil {
				return err
			}
		}
	}

	// Start up remaining nodes
	for _, node := range nodeQueue {
		logger.Info(fmt.Sprintf("Starting node %v at height %v...", node.Name, node.StartAt))
		if _, err := mainNode.WaitFor(node.StartAt, 1*time.Minute); err != nil {
			return err
		}
		if err := execCompose(cli.testnet.Dir, "up", "-d", node.Name); err != nil {
			return err
		}
		status, err := node.WaitFor(node.StartAt, 1*time.Minute)
		if err != nil {
			return err
		}
		logger.Info(fmt.Sprintf("Node %v up on http://127.0.0.1:%v at height %v",
			node.Name, node.ProxyPort, status.SyncInfo.LatestBlockHeight))
	}

	return nil
}

// Perturbs a running testnet.
func (cli *CLI) Perturb() error {
	lastHeight := int64(0)
	for _, node := range cli.testnet.Nodes {
		for _, perturbation := range node.Perturbations {
			switch perturbation {
			case e2e.PerturbationDisconnect:
				logger.Info(fmt.Sprintf("Disconnecting node %v...", node.Name))
				if err := execDocker("network", "disconnect", cli.testnet.Name+"_"+cli.testnet.Name, node.Name); err != nil {
					return err
				}
				time.Sleep(5 * time.Second)
				if err := execDocker("network", "connect", cli.testnet.Name+"_"+cli.testnet.Name, node.Name); err != nil {
					return err
				}

			case e2e.PerturbationKill:
				logger.Info(fmt.Sprintf("Killing node %v...", node.Name))
				if err := execCompose(cli.testnet.Dir, "kill", "-s", "SIGKILL", node.Name); err != nil {
					return err
				}
				if err := execCompose(cli.testnet.Dir, "start", node.Name); err != nil {
					return err
				}

			case e2e.PerturbationPause:
				logger.Info(fmt.Sprintf("Pausing node %v...", node.Name))
				if err := execCompose(cli.testnet.Dir, "pause", node.Name); err != nil {
					return err
				}
				time.Sleep(5 * time.Second)
				if err := execCompose(cli.testnet.Dir, "unpause", node.Name); err != nil {
					return err
				}

			case e2e.PerturbationRestart:
				logger.Info(fmt.Sprintf("Restarting node %v...", node.Name))
				if err := execCompose(cli.testnet.Dir, "restart", node.Name); err != nil {
					return err
				}

			default:
				return fmt.Errorf("unexpected perturbation %q", perturbation)
			}

			status, err := node.WaitFor(0, 10*time.Second)
			if err != nil {
				return err
			}
			logger.Info(fmt.Sprintf("Node %v recovered at height %v", node.Name, status.SyncInfo.LatestBlockHeight))
			if status.SyncInfo.LatestBlockHeight > lastHeight {
				lastHeight = status.SyncInfo.LatestBlockHeight
			}
		}
	}

	// Wait for another 10 blocks to be produced.
	if lastHeight > 0 {
		for _, node := range cli.testnet.Nodes {
			if node.Mode == e2e.ModeValidator {
				waitFor := lastHeight + 10
				logger.Info(fmt.Sprintf("Waiting for height %v...", waitFor))

				_, err := node.WaitFor(uint64(waitFor), 1*time.Minute)
				if err != nil {
					return err
				}
				break
			}
		}
	}

	return nil
}
