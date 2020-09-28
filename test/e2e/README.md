# End-to-End Tests

## Testnet Manifests

Testnets are specified as TOML manifests. For an example see [`networks/ci.toml`](networks/ci.toml), and for documentation see [`pkg/manifest.go`](pkg/manifest.go).

## Enabling IPv6

Docker does not enable IPv6 by default. To do so, enter the following in `daemon.json` (or in the Docker for Mac UI under Preferences → Docker Engine):

```json
{
  "ipv6": true,
  "fixed-cidr-v6": "2001:db8:1::/64"
}
```
