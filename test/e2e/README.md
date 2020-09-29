# End-to-End Tests

Spins up and tests Tendermint networks in Docker Compose based on a testnet manifest file. The following commands will run the CI testnet:

```sh
$ make docker
$ make runner
$ ./build/runner -f networks/ci.toml
```

This creates a testnet named `ci` under `networks/ci` (given by manifest filename), spins up Docker containers, and runs tests against them.

## Testnet Manifests

Testnets are specified as TOML manifests. For an example see [`networks/ci.toml`](networks/ci.toml), and for documentation see [`pkg/manifest.go`](pkg/manifest.go).

## Test Stages

The test runner has the following stages, which can also be executed explicitly by running `./build/runner -f <manifest> <stage>`:

* `setup`: generates configuration files.

* `start`: starts Docker containers.

* `perturb`: runs any requested perturbations (e.g. node restarts or network disconnects).

* `stop`: stops Docker containers.

* `cleanup`: removes configuration files and Docker containers/networks.

## Enabling IPv6

Docker does not enable IPv6 by default. To do so, enter the following in `daemon.json` (or in the Docker for Mac UI under Preferences → Docker Engine):

```json
{
  "ipv6": true,
  "fixed-cidr-v6": "2001:db8:1::/64"
}
```
