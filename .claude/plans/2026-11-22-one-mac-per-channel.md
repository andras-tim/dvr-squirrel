# One MAC address per channel

## Problem

UniFi Protect keeps adopting the channels again and again: next to each adopted camera it lists another one on the
same address with "Click to Adopt".

Observed in a live test with one `listen_ip` per channel, the addresses first on one interface, later each on a macvlan
of its own:

- Before adoption Protect keys a discovered camera by the MAC address it resolves for the camera's IP (ARP): channels
  whose addresses share an interface show up as a single camera, whatever their ports or addresses.
- After adoption it keys the camera by the `HwAddress` of `GetNetworkInterfaces`. The bridge reports a MAC derived from
  the serial there, which differs from the MAC on the wire.
- So every discovery finds a camera whose ARP MAC matches no adopted one, and lists it as new.

## Decisions

- **The reported MAC is the one on the wire:** a channel with a `listen_ip` reports the MAC of the interface holding
  that address; NVRs see the same MAC in ARP and in `GetNetworkInterfaces`. The interface is looked up when the channel
  starts; a lookup failure falls back to the derived MAC with a warning.
- **`mac` per channel** overrides it, for setups where the address sits behind something the bridge cannot see (a
  container network, a NAT). Only in `[[channel]]`, never in `[defaults]`: a MAC is unique per camera. Two enabled
  channels with the same configured MAC are a config error.
- **Channels without a `listen_ip`** keep the derived MAC; they share the host's MAC on the wire anyway, and an NVR
  that keys by MAC merges them (documented).
- **Two channels resolving to the same interface MAC** start, with a warning: several addresses on one interface are a
  valid setup for NVRs that key by address.
- **One interface per channel is the deployment's job** (e.g. a macvlan with a fixed MAC per channel); the bridge does
  not create interfaces, as that needs `CAP_NET_ADMIN`. The README describes the setup.

## Also in this round (found in the same deployment)

- The example unit: `RestrictAddressFamilies` lacks `AF_NETLINK`, which the discovery's interface lookup needs (the
  daemon exits with "address family not supported by protocol"); systemd 249 (Ubuntu 22.04) does not hand
  `LoadCredential` files to `ExecStartPre`, so the `--check-config` pre-start fails there: drop it, the daemon checks
  the config at start anyway.
- Scopes: `url.PathEscape` turns a comma into `%2C`, and Protect shows that as is ("DVR 500 series%2C 4 channels");
  escape only what a scope path segment cannot hold.
- README: "Protect identifies a camera by its IP address" was wrong; replace it with the MAC findings above.

## Release

v0.2.0 (a new config key, a changed reported MAC: cameras adopted with v0.1.0 need adopting again once).
