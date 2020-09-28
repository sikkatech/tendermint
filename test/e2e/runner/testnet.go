package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"time"

	"github.com/tendermint/tendermint/crypto"
	"github.com/tendermint/tendermint/crypto/ed25519"
	rpc "github.com/tendermint/tendermint/rpc/client"
	rpchttp "github.com/tendermint/tendermint/rpc/client/http"
	rpctypes "github.com/tendermint/tendermint/rpc/core/types"
)

var (
	ip4network     *net.IPNet
	ip6network     *net.IPNet
	proxyPortFirst uint32 = 5701
)

func init() {
	var err error
	_, ip4network, err = net.ParseCIDR("10.200.0.0/16")
	if err != nil {
		panic(err)
	}
	_, ip6network, err = net.ParseCIDR("fd80:b10c::/48")
	if err != nil {
		panic(err)
	}
}

// Testnet represents a single testnet
type Testnet struct {
	Name             string
	IP               *net.IPNet
	InitialHeight    uint64
	InitialState     map[string]string
	ValidatorUpdates map[uint64]map[string]uint8
	Nodes            []*Node
}

// Node represents a Tendermint node in a testnet
type Node struct {
	Name             string
	Mode             string
	Key              crypto.PrivKey
	IP               net.IP
	ProxyPort        uint32
	StartAt          uint64
	FastSync         string
	StateSync        bool
	Database         string
	ABCIProtocol     string
	PrivvalProtocol  string
	PersistInterval  uint64
	SnapshotInterval uint64
	RetainBlocks     uint64
	Seeds            []*Node
	PersistentPeers  []*Node
	Perturb          []string
}

// NewTestnet creates a testnet from a manifest.
func NewTestnet(name string, manifest Manifest) (*Testnet, error) {
	testnet := &Testnet{
		Name:             name,
		IP:               ip4network,
		InitialHeight:    1,
		InitialState:     manifest.InitialState,
		ValidatorUpdates: map[uint64]map[string]uint8{},
		Nodes:            []*Node{},
	}
	if manifest.InitialHeight > 0 {
		testnet.InitialHeight = manifest.InitialHeight
	}
	if manifest.IPv6 {
		testnet.IP = ip6network
	}

	ip := nextIP(nextIP(testnet.IP.IP)) // increment twice to skip gateway address
	proxyPort := proxyPortFirst
	nodeNames := []string{}
	for name := range manifest.Nodes {
		nodeNames = append(nodeNames, name)
	}
	sort.Strings(nodeNames)
	for _, name := range nodeNames {
		nodeManifest := manifest.Nodes[name]
		node, err := NewNode(name, ip, proxyPort, nodeManifest)
		if err != nil {
			return nil, err
		}
		testnet.Nodes = append(testnet.Nodes, node)
		ip = nextIP(ip)
		proxyPort++
	}

	// We do a second pass to set up seeds and persistent peers, which allows graph cycles.
	for _, node := range testnet.Nodes {
		nodeManifest, ok := manifest.Nodes[node.Name]
		if !ok {
			return nil, fmt.Errorf("failed to look up manifest for node %q", node.Name)
		}
		for _, seedName := range nodeManifest.Seeds {
			seed := testnet.LookupNode(seedName)
			if seed == nil {
				return nil, fmt.Errorf("unknown seed %q for node %q", seedName, node.Name)
			}
			node.Seeds = append(node.Seeds, seed)
		}
		for _, peerName := range nodeManifest.PersistentPeers {
			peer := testnet.LookupNode(peerName)
			if peer == nil {
				return nil, fmt.Errorf("unknown persistent peer %q for node %q", peerName, node.Name)
			}
			node.PersistentPeers = append(node.PersistentPeers, peer)
		}
	}

	for heightStr, validators := range manifest.ValidatorUpdates {
		height, err := strconv.Atoi(heightStr)
		if err != nil {
			return nil, fmt.Errorf("invalid validator update height %q: %w", height, err)
		}
		valUpdate := map[string]uint8{}
		for name, power := range validators {
			valUpdate[name] = power
		}
		testnet.ValidatorUpdates[uint64(height)] = valUpdate
	}

	if err := testnet.Validate(); err != nil {
		return nil, err
	}
	return testnet, nil
}

// NewNode creates a new testnet node from a node manifest.
func NewNode(name string, ip net.IP, proxyPort uint32, nodeManifest ManifestNode) (*Node, error) {
	node := &Node{
		Name:             name,
		Key:              ed25519.GenPrivKey(),
		IP:               ip,
		ProxyPort:        proxyPort,
		Mode:             "validator",
		StartAt:          nodeManifest.StartAt,
		FastSync:         nodeManifest.FastSync,
		StateSync:        nodeManifest.StateSync,
		Database:         "goleveldb",
		ABCIProtocol:     "unix",
		PrivvalProtocol:  "file",
		PersistInterval:  1,
		SnapshotInterval: nodeManifest.SnapshotInterval,
		RetainBlocks:     nodeManifest.RetainBlocks,
		Perturb:          nodeManifest.Perturb,
	}
	if nodeManifest.Mode != "" {
		node.Mode = nodeManifest.Mode
	}
	if nodeManifest.Database != "" {
		node.Database = nodeManifest.Database
	}
	if nodeManifest.ABCIProtocol != "" {
		node.ABCIProtocol = nodeManifest.ABCIProtocol
	}
	if nodeManifest.PrivvalProtocol != "" {
		node.PrivvalProtocol = nodeManifest.PrivvalProtocol
	}
	if nodeManifest.PersistInterval != nil {
		node.PersistInterval = *nodeManifest.PersistInterval
	}
	return node, nil
}

// Validate validates a testnet.
func (t Testnet) Validate() error {
	if t.Name == "" {
		return errors.New("network has no name")
	}
	if t.IP == nil {
		return errors.New("network has no IP")
	}
	if len(t.Nodes) == 0 {
		return errors.New("network has no nodes")
	}
	for _, node := range t.Nodes {
		if err := node.Validate(t); err != nil {
			return fmt.Errorf("invalid node %q: %w", node.Name, err)
		}
	}
	for height, valUpdate := range t.ValidatorUpdates {
		for name := range valUpdate {
			if t.LookupNode(name) == nil {
				return fmt.Errorf("unknown node %q for validator update at height %v", name, height)
			}
		}
	}

	return nil
}

// Validate validates a node.
func (n Node) Validate(testnet Testnet) error {
	if n.Name == "" {
		return errors.New("node has no name")
	}
	if n.IP == nil {
		return errors.New("node has no IP address")
	}
	if !testnet.IP.Contains(n.IP) {
		return fmt.Errorf("node IP %v is not in testnet network %v", n.IP, testnet.IP)
	}
	if n.ProxyPort > 0 {
		if n.ProxyPort <= 1024 {
			return fmt.Errorf("local port %v must be >1024", n.ProxyPort)
		}
		for _, peer := range testnet.Nodes {
			if peer.Name != n.Name && peer.ProxyPort == n.ProxyPort {
				return fmt.Errorf("peer %q also has local port %v", peer.Name, n.ProxyPort)
			}
		}
	}
	switch n.Mode {
	case "validator", "seed", "full":
	default:
		return fmt.Errorf("invalid mode %q", n.Mode)
	}
	switch n.FastSync {
	case "", "v0", "v1", "v2":
	default:
		return fmt.Errorf("invalid fast sync setting %q", n.FastSync)
	}
	switch n.Database {
	case "goleveldb", "cleveldb", "boltdb", "rocksdb", "badgerdb":
	default:
		return fmt.Errorf("invalid database setting %q", n.Database)
	}
	switch n.ABCIProtocol {
	case "unix", "tcp", "grpc":
	default:
		return fmt.Errorf("invalid ABCI protocol setting %q", n.ABCIProtocol)
	}
	switch n.PrivvalProtocol {
	case "file", "unix", "tcp":
	default:
		return fmt.Errorf("invalid privval protocol setting %q", n.PrivvalProtocol)
	}

	if n.StateSync && n.StartAt == 0 {
		return errors.New("state synced nodes cannot start at the initial height")
	}
	if n.PersistInterval == 0 && n.RetainBlocks > 0 {
		return errors.New("persist_interval=0 requires retain_blocks=0")
	}
	if n.PersistInterval > 1 && n.RetainBlocks > 0 && n.RetainBlocks < n.PersistInterval {
		return errors.New("persist_interval must be less than or equal to retain_blocks")
	}
	if n.SnapshotInterval > 0 && n.RetainBlocks > 0 && n.RetainBlocks < n.SnapshotInterval {
		return errors.New("snapshot_interval must be less than er equal to retain_blocks")
	}

	for _, perturbation := range n.Perturb {
		switch perturbation {
		case "restart", "kill", "disconnect", "pause":
		default:
			return fmt.Errorf("invalid node perturbation %q", perturbation)
		}
	}
	return nil
}

// LookupNode looks up a node by name. For now, simply do a linear search.
func (t Testnet) LookupNode(name string) *Node {
	for _, node := range t.Nodes {
		if node.Name == name {
			return node
		}
	}
	return nil
}

// IPv6 returns true if the testnet is an IPv6 network.
func (t Testnet) IPv6() bool {
	return t.IP.IP.To4() == nil
}

// Address returns a P2P endpoint address for the node.
func (n Node) Address() string {
	ip := n.IP.String()
	if n.IP.To4() == nil {
		// IPv6 addresses must be wrapped in [] to avoid conflict with : port separator
		ip = fmt.Sprintf("[%v]", ip)
	}
	return fmt.Sprintf("%v:26656", ip)
}

// Address returns an RPC endpoint address for the node.
func (n Node) AddressRPC() string {
	ip := n.IP.String()
	if n.IP.To4() == nil {
		// IPv6 addresses must be wrapped in [] to avoid conflict with : port separator
		ip = fmt.Sprintf("[%v]", ip)
	}
	return fmt.Sprintf("%v:26657", ip)
}

// Address returns a P2P endpoint address for the node, including the node ID.
func (n Node) AddressWithID() string {
	return fmt.Sprintf("%x@%v", n.Key.PubKey().Address().Bytes(), n.Address())
}

// Client returns an RPC client for a node.
func (n Node) Client() (rpc.Client, error) {
	return rpchttp.New(fmt.Sprintf("http://127.0.0.1:%v", n.ProxyPort), "/websocket")
}

// LastBlock fetches the last block.
func (n Node) LastBlock() (*rpctypes.ResultBlock, error) {
	client, err := n.Client()
	if err != nil {
		return nil, err
	}
	return client.Block(context.Background(), nil)
}

// WaitFor waits for the node to become available and catch up to the given block height.
func (n Node) WaitFor(height uint64, timeout time.Duration) (*rpctypes.ResultStatus, error) {
	client, err := n.Client()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for {
		status, err := client.Status(ctx)
		if err == nil && status.SyncInfo.LatestBlockHeight >= int64(height) {
			return status, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// nextIP increments the IP address and returns it
func nextIP(ip net.IP) net.IP {
	next := make([]byte, len(ip))
	copy(next, ip)
	for i := len(next) - 1; i >= 0; i-- {
		next[i]++
		if next[i] != 0 {
			break
		}
	}
	return next
}
