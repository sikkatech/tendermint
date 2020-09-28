package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
			if err := cli.Cleanup(); err != nil {
				return err
			}
			if err := cli.Setup(); err != nil {
				return err
			}
			if err := cli.Start(); err != nil {
				return err
			}
			if err := cli.Perturb(); err != nil {
				return err
			}
			if err := cli.Cleanup(); err != nil {
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
			return cli.Setup()
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
			return cli.Stop()
		},
	})

	cli.root.AddCommand(&cobra.Command{
		Use:   "logs",
		Short: "Shows the testnet logs",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cli.Logs()
		},
	})

	cli.root.AddCommand(&cobra.Command{
		Use:   "cleanup",
		Short: "Removes the testnet directory",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cli.Cleanup()
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

// runCompose runs a Docker Compose command.
func (cli *CLI) runCompose(args ...string) error {
	args = append([]string{"-f", filepath.Join(cli.testnet.Dir, "docker-compose.yml")}, args...)
	cmd := exec.Command("docker-compose", args...)
	out, err := cmd.CombinedOutput()
	switch err := err.(type) {
	case nil:
		return nil
	case *exec.ExitError:
		return fmt.Errorf("failed to run docker-compose %q:\n%v", args, string(out))
	default:
		return err
	}
}

// runCompose runs a Docker Compose command and displays its output.
func (cli *CLI) runComposeOutput(args ...string) error {
	args = append([]string{"-f", filepath.Join(cli.testnet.Dir, "docker-compose.yml")}, args...)
	cmd := exec.Command("docker-compose", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// runDocker runs a Docker command.
func (cli *CLI) runDocker(args ...string) error {
	cmd := exec.Command("docker", args...)
	out, err := cmd.CombinedOutput()
	switch err := err.(type) {
	case nil:
		return nil
	case *exec.ExitError:
		return fmt.Errorf("failed to run docker %q:\n%v", args, string(out))
	default:
		return err
	}
}

// Setup generates the testnet configuration.
func (cli *CLI) Setup() error {
	logger.Info(fmt.Sprintf("Generating testnet files in %q", cli.testnet.Dir))
	err := Setup(cli.testnet, cli.testnet.Dir)
	if err != nil {
		return err
	}
	return nil
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
		if err := cli.runCompose("up", "-d", node.Name); err != nil {
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
			err = UpdateConfigStateSync(cli.testnet.Dir, node, lastBlock.Block.Height, lastBlock.BlockID.Hash.Bytes())
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
		if err := cli.runCompose("up", "-d", node.Name); err != nil {
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
	for _, node := range cli.testnet.Nodes {
		for _, perturbation := range node.Perturbations {
			switch perturbation {
			case e2e.PerturbationDisconnect:
				logger.Info(fmt.Sprintf("Disconnecting node %v...", node.Name))
				if err := cli.runDocker("network", "disconnect", cli.testnet.Name+"_"+cli.testnet.Name, node.Name); err != nil {
					return err
				}
				time.Sleep(5 * time.Second)
				if err := cli.runDocker("network", "connect", cli.testnet.Name+"_"+cli.testnet.Name, node.Name); err != nil {
					return err
				}

			case e2e.PerturbationKill:
				logger.Info(fmt.Sprintf("Killing node %v...", node.Name))
				if err := cli.runCompose("kill", "-s", "SIGKILL", node.Name); err != nil {
					return err
				}
				if err := cli.runCompose("start", node.Name); err != nil {
					return err
				}

			case e2e.PerturbationPause:
				logger.Info(fmt.Sprintf("Pausing node %v...", node.Name))
				if err := cli.runCompose("pause", node.Name); err != nil {
					return err
				}
				time.Sleep(5 * time.Second)
				if err := cli.runCompose("unpause", node.Name); err != nil {
					return err
				}

			case e2e.PerturbationRestart:
				logger.Info(fmt.Sprintf("Restarting node %v...", node.Name))
				if err := cli.runCompose("restart", node.Name); err != nil {
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
		}
	}
	return nil
}

// Logs outputs testnet logs.
func (cli *CLI) Logs() error {
	return cli.runComposeOutput("logs", "--follow")
}

// Stop stops the testnet and removes the containers.
func (cli *CLI) Stop() error {
	logger.Info("Stopping testnet")
	return cli.runCompose("down")
}

// Cleanup removes the Docker Compose containers and testnet directory.
func (cli *CLI) Cleanup() error {
	if cli.testnet.Dir == "" {
		return errors.New("no directory set")
	}
	_, err := os.Stat(cli.testnet.Dir)
	if os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}

	logger.Info("Removing Docker containers and networks")
	err = cli.runCompose("down")
	if err != nil {
		return err
	}

	logger.Info(fmt.Sprintf("Removing testnet directory %q", cli.testnet.Dir))
	err = os.RemoveAll(cli.testnet.Dir)
	if err != nil {
		return err
	}
	return nil
}
