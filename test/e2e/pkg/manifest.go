package e2e

import (
	"fmt"

	"github.com/BurntSushi/toml"
)

// Manifest represents a TOML testnet manifest.
type Manifest struct {
	// Seed is a random seed used for generating e.g. node keys and IP networks.
	// If not given, a fixed seed is used instead.
	Seed int64

	// IPv6 uses IPv6 networking instead of IPv4. Defaults to IPv4.
	IPv6 bool `toml:"ipv6"`

	// InitialHeight specifies the initial block height, set in genesis. Defaults to 1.
	InitialHeight uint64 `toml:"initial_height"`

	// InitialState is an initial set of key/value pairs for the application,
	// set in genesis. Defaults to nothing.
	InitialState map[string]string `toml:"initial_state"`

	// Validators is the initial validator set in genesis. Defaults to all nodes
	// with mode=validator given power 100. Explicitly specifying an empty list
	// will start with no validators in genesis.
	Validators *[]string

	// ValidatorUpdates is a map of heights to validator names and their power,
	// and will be returned by the ABCI application. For example, the following
	// changes the power of validator01 and validator02 at height 1000:
	//
	// [validator_update.1000]
	// validator01 = 20
	// validator02 = 10
	//
	// Specifying height 0 causes the validator updates to be returned during
	// InitChain. The application returns the validator updates as-is, i.e.
	// removing a validator must be done by returning it with power 0, and
	// any validators not specified are not changed.
	ValidatorUpdates map[string]map[string]uint8 `toml:"validator_update"`

	// Nodes specifies the network nodes. At least one node must be given.
	Nodes map[string]ManifestNode `toml:"node"`
}

// ManifestNode represents a node in a testnet manifest.
type ManifestNode struct {
	Mode             string
	StartAt          uint64 `toml:"start_at"`
	FastSync         string `toml:"fast_sync"`
	StateSync        bool   `toml:"state_sync"`
	Database         string
	ABCIProtocol     string  `toml:"abci_protocol"`
	PersistInterval  *uint64 `toml:"persist_interval"`
	SnapshotInterval uint64  `toml:"snapshot_interval"`
	RetainBlocks     uint64  `toml:"retain_blocks"`
	PrivvalProtocol  string  `toml:"privval_protocol"`
	Seeds            []string
	PersistentPeers  []string `toml:"persistent_peers"`
	Perturb          []string
}

// LoadManifest loads a testnet manifest from a file.
func LoadManifest(file string) (Manifest, error) {
	manifest := Manifest{}
	_, err := toml.DecodeFile(file, &manifest)
	if err != nil {
		return manifest, fmt.Errorf("failed to load testnet manifest %q: %w", file, err)
	}
	return manifest, nil
}
