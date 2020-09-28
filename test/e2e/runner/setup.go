// nolint: gosec,goconst
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/tendermint/tendermint/config"
	"github.com/tendermint/tendermint/crypto/ed25519"
	"github.com/tendermint/tendermint/p2p"
	"github.com/tendermint/tendermint/privval"
	e2e "github.com/tendermint/tendermint/test/e2e/pkg"
	"github.com/tendermint/tendermint/types"
)

// Setup sets up testnet configuration in a directory.
func Setup(testnet *e2e.Testnet, dir string) error {
	if err := os.MkdirAll(dir, os.ModePerm); err != nil {
		return err
	}

	compose, err := MakeDockerCompose(testnet)
	if err != nil {
		return err
	}
	if err := ioutil.WriteFile(filepath.Join(dir, "docker-compose.yml"), compose, 0644); err != nil {
		return err
	}

	genesis, err := MakeGenesis(testnet)
	if err != nil {
		return err
	}
	for _, node := range testnet.Nodes {
		nodeDir := filepath.Join(dir, node.Name)
		cfg, err := MakeConfig(testnet, node)
		if err != nil {
			return err
		}
		appCfg, err := MakeAppConfig(testnet, node)
		if err != nil {
			return err
		}

		if err := os.MkdirAll(nodeDir, 0755); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(nodeDir, "config"), 0755); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(nodeDir, "data"), 0755); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(nodeDir, "data", "app"), 0755); err != nil {
			return err
		}
		if err := genesis.SaveAs(filepath.Join(nodeDir, "config", "genesis.json")); err != nil {
			return err
		}
		config.WriteConfigFile(filepath.Join(nodeDir, "config", "config.toml"), cfg) // panics
		if err := ioutil.WriteFile(filepath.Join(nodeDir, "config", "app.toml"), appCfg, 0644); err != nil {
			return err
		}
		if err := genesis.SaveAs(filepath.Join(nodeDir, "config", "genesis.json")); err != nil {
			return err
		}
		if err := MakeNodeKey(node).SaveAs(filepath.Join(nodeDir, "config", "node_key.json")); err != nil {
			return err
		}

		(privval.NewFilePV(node.Key,
			filepath.Join(nodeDir, "config", "priv_validator_key.json"),
			filepath.Join(nodeDir, "data", "priv_validator_state.json"),
		)).Save()
		// Set up a dummy validator. Tendermint requires a file PV even when configured with
		// a remote KMS, so we just give it this dummy to make sure it actually uses the remote.
		(privval.NewFilePV(ed25519.GenPrivKey(),
			filepath.Join(nodeDir, "config", "dummy_validator_key.json"),
			filepath.Join(nodeDir, "data", "dummy_validator_state.json"),
		)).Save()
	}

	return nil
}

// MakeDockerCompose generates a Docker Compose config for a testnet.
// Must use version 2 Docker Compose format, to support IPv6.
func MakeDockerCompose(testnet *e2e.Testnet) ([]byte, error) {
	tmpl, err := template.New("docker-compose").Parse(`version: '2.4'

networks:
  {{ .Name }}:
    driver: bridge
{{- if .IPv6 }}
    enable_ipv6: true
{{- end }}
    ipam:
      driver: default
      config:
      - subnet: {{ .IP }}

services:
{{- range .Nodes }}
  {{ .Name }}:
    container_name: {{ .Name }}
    image: tendermint/e2e-node
    init: true
    ports:
    - 26656
    - {{ if .ProxyPort }}{{ .ProxyPort }}:{{ end }}26657
    volumes:
    - ./{{ .Name }}:/tendermint
    networks:
      {{ $.Name }}:
        ipv{{ if $.IPv6 }}6{{ else }}4{{ end}}_address: {{ .IP }}

{{end}}`)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	err = tmpl.Execute(&buf, testnet)
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// MakeGenesis generates a genesis document.
func MakeGenesis(testnet *e2e.Testnet) (types.GenesisDoc, error) {
	genesis := types.GenesisDoc{
		GenesisTime:     time.Now(),
		ChainID:         testnet.Name,
		ConsensusParams: types.DefaultConsensusParams(),
		InitialHeight:   int64(testnet.InitialHeight),
	}
	for validator, power := range testnet.Validators {
		genesis.Validators = append(genesis.Validators, types.GenesisValidator{
			Name:    validator.Name,
			Address: validator.Key.PubKey().Address(),
			PubKey:  validator.Key.PubKey(),
			Power:   power,
		})
	}
	// The validator set will be sorted internally by Tendermint ranked by power,
	// but we sort it here as well so that all genesis files are identical.
	sort.Slice(genesis.Validators, func(i, j int) bool {
		return strings.Compare(genesis.Validators[i].Name, genesis.Validators[j].Name) == -1
	})
	if len(testnet.InitialState) > 0 {
		appState, err := json.Marshal(testnet.InitialState)
		if err != nil {
			return genesis, err
		}
		genesis.AppState = appState
	}
	return genesis, genesis.ValidateAndComplete()
}

// MakeConfig generates a Tendermint config for a node.
func MakeConfig(testnet *e2e.Testnet, node *e2e.Node) (*config.Config, error) {
	cfg := config.DefaultConfig()
	cfg.Moniker = node.Name
	cfg.ProxyApp = "tcp://127.0.0.1:30000"
	cfg.RPC.ListenAddress = "tcp://0.0.0.0:26657"
	cfg.P2P.ExternalAddress = fmt.Sprintf("tcp://%v", node.Address())
	cfg.P2P.AddrBookStrict = false
	cfg.DBBackend = node.Database
	cfg.StateSync.DiscoveryTime = 5 * time.Second

	switch node.ABCIProtocol {
	case "unix":
		cfg.ProxyApp = "unix:///var/run/app.sock"
	case "tcp":
		cfg.ProxyApp = "tcp://127.0.0.1:30000"
	case "grpc":
		cfg.ProxyApp = "tcp://127.0.0.1:30000"
		cfg.ABCI = "grpc"
	default:
		return nil, fmt.Errorf("unexpected ABCI protocol setting %q", node.ABCIProtocol)
	}

	// Tendermint errors if it does not have a privval key set up, regardless of whether
	// it's actually needed (e.g. for remote KMS or non-validators). We set up a dummy
	// key here by default, and use the real key for actual validators that should use
	// the file privval.
	cfg.PrivValidatorListenAddr = ""
	cfg.PrivValidatorKey = "config/dummy_validator_key.json"
	cfg.PrivValidatorState = "data/dummy_validator_state.json"

	switch node.Mode {
	case "validator":
		switch node.PrivvalProtocol {
		case "file":
			cfg.PrivValidatorKey = "config/priv_validator_key.json"
			cfg.PrivValidatorState = "data/priv_validator_state.json"
		case "unix":
			cfg.PrivValidatorListenAddr = "unix:///var/run/privval.sock"
		case "tcp":
			cfg.PrivValidatorListenAddr = "tcp://0.0.0.0:27559"
		default:
			return nil, fmt.Errorf("invalid privval protocol setting %q", node.PrivvalProtocol)
		}
	case "seed":
		cfg.P2P.SeedMode = true
		cfg.P2P.PexReactor = true
	case "full":
		// Don't need to do anything, since we're using a dummy privval key by default.
	default:
		return nil, fmt.Errorf("unexpected mode %q", node.Mode)
	}

	if node.FastSync == "" {
		cfg.FastSyncMode = false
	} else {
		cfg.FastSync.Version = node.FastSync
	}

	if node.StateSync {
		cfg.StateSync.Enable = true
		cfg.StateSync.RPCServers = []string{}
		for _, peer := range testnet.Nodes {
			switch {
			case peer.Name == node.Name:
				continue
			case peer.StartAt > 0:
				continue
			case peer.RetainBlocks > 0:
				continue
			default:
				cfg.StateSync.RPCServers = append(cfg.StateSync.RPCServers, peer.AddressRPC())
			}
		}
		if len(cfg.StateSync.RPCServers) < 2 {
			return nil, errors.New("unable to find 2 suitable state sync RPC servers")
		}
	}

	cfg.P2P.Seeds = ""
	for _, seed := range node.Seeds {
		if len(cfg.P2P.Seeds) > 0 {
			cfg.P2P.Seeds += ","
		}
		cfg.P2P.Seeds += seed.AddressWithID()
	}
	cfg.P2P.PersistentPeers = ""
	for _, peer := range node.PersistentPeers {
		if len(cfg.P2P.PersistentPeers) > 0 {
			cfg.P2P.PersistentPeers += ","
		}
		cfg.P2P.PersistentPeers += peer.AddressWithID()
	}
	return cfg, nil
}

// MakeAppConfig generates an ABCI application config for a node.
func MakeAppConfig(testnet *e2e.Testnet, node *e2e.Node) ([]byte, error) {
	cfg := map[string]interface{}{
		"chain_id":          testnet.Name,
		"dir":               "data/app",
		"listen":            "unix:///var/run/app.sock",
		"grpc":              false,
		"persist_interval":  node.PersistInterval,
		"snapshot_interval": node.SnapshotInterval,
		"retain_blocks":     node.RetainBlocks,
	}
	switch node.ABCIProtocol {
	case "unix":
		cfg["listen"] = "unix:///var/run/app.sock"
	case "tcp":
		cfg["listen"] = "tcp://127.0.0.1:30000"
	case "grpc":
		cfg["listen"] = "tcp://127.0.0.1:30000"
		cfg["grpc"] = true
	default:
		return nil, fmt.Errorf("unexpected ABCI protocol setting %q", node.ABCIProtocol)
	}
	switch node.PrivvalProtocol {
	case "file":
	case "tcp":
		cfg["privval_server"] = "tcp://127.0.0.1:27559"
		cfg["privval_key"] = "config/priv_validator_key.json"
		cfg["privval_state"] = "data/priv_validator_state.json"
	case "unix":
		cfg["privval_server"] = "unix:///var/run/privval.sock"
		cfg["privval_key"] = "config/priv_validator_key.json"
		cfg["privval_state"] = "data/priv_validator_state.json"
	default:
		return nil, fmt.Errorf("unexpected privval protocol setting %q", node.PrivvalProtocol)
	}

	if len(testnet.ValidatorUpdates) > 0 {
		validatorUpdates := map[string]map[string]int64{}
		for height, validators := range testnet.ValidatorUpdates {
			updateVals := map[string]int64{}
			for node, power := range validators {
				updateVals[base64.StdEncoding.EncodeToString(node.Key.PubKey().Bytes())] = power
			}
			validatorUpdates[fmt.Sprintf("%v", height)] = updateVals
		}
		cfg["validator_update"] = validatorUpdates
	}

	var buf bytes.Buffer
	err := toml.NewEncoder(&buf).Encode(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to generate app config: %w", err)
	}
	return buf.Bytes(), nil
}

// MakeNodeKey generates a node key.
func MakeNodeKey(node *e2e.Node) *p2p.NodeKey {
	return &p2p.NodeKey{PrivKey: node.Key}
}

// UpdateConfigStateSync updates the state sync config for a node.
func UpdateConfigStateSync(dir string, node *e2e.Node, height int64, hash []byte) error {
	cfgPath := filepath.Join(dir, node.Name, "config", "config.toml")

	// FIXME Apparently there's no function to simply load a config file without
	// involving the entire Viper apparatus, so we'll just resort to regexps.
	bz, err := ioutil.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	bz = regexp.MustCompile(`(?m)^trust_height =.*`).ReplaceAll(bz, []byte(fmt.Sprintf(`trust_height = %v`, height)))
	bz = regexp.MustCompile(`(?m)^trust_hash =.*`).ReplaceAll(bz, []byte(fmt.Sprintf(`trust_hash = "%X"`, hash)))
	return ioutil.WriteFile(cfgPath, bz, 0644)
}
