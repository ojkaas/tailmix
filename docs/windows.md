# tailmix on Windows

tailmix connects one Windows computer to several Tailscale tailnets at the same
time. It runs as the `tailmixd` Windows service with its own Wintun network
adapter, and a tray app gives you the everyday controls of the official
client.

Windows 10 (1809) or later and an Administrator account are required.

## Install

1. Download `tailmix-windows-amd64.zip` (or `-arm64`) and extract it.
2. Right-click `install.ps1` → **Run with PowerShell**, or from a terminal:

   ```powershell
   powershell -ExecutionPolicy Bypass -File .\install.ps1
   ```

   The script asks for elevation, installs to `C:\Program Files\tailmix`,
   registers and starts the `tailmixd` service, adds `tailmix` to the system
   `PATH`, allows tailmixd's inbound UDP through Windows Defender Firewall
   (for direct connections; without it traffic is relayed through DERP), and
   starts the tray app.

Rerun `install.ps1` from a newer release to upgrade. Your tailnets stay
logged in. tailmix does not update itself on Windows.

## Use the tray

The tray icon is a 3×3 grid of dots. Each row stands for one of your first
three tailnets and lights up in its own colour while that tailnet is
connected. A yellow badge means a tailnet is starting or waiting for login; a
red badge means one has failed. Grey dots mean tailmix is disconnected or
the service is not running.

Click the icon for the menu:

- **Connect / Disconnect** starts or stops every enabled tailnet.
- One entry per tailnet shows its state. Its submenu has **Log in…**,
  **Copy my IP**, **Copy my name**, **Devices** (click one to copy its name),
  **Admin console**, **Enabled**, **Restart** and **Log out**.
- **Exit node** lists the exit nodes of every connected tailnet. tailmix uses
  at most one exit node at a time.
- **Add tailnet…** asks for a profile name, adds it, and opens the login page.
- **Run at login** starts the tray when you sign in.

Device names are MagicDNS names such as `nas.tail1234.ts.net`. Use names
rather than `100.x` addresses: tailmix gives every device its own local
address so that tailnets with overlapping addresses can coexist, and MagicDNS
answers with that local address.

## Use the CLI

From a new terminal:

```powershell
tailmix profiles add work
tailmix ts --profile work up        # prints a login URL
tailmix profiles add home
tailmix ts --profile home up
tailmix status
```

`tailmix ts --profile NAME ...` runs any `tailscale` subcommand against that
tailnet. See the main README for routes, DNS and exit nodes.

Commands that change settings require an Administrator account; an ordinary
(unelevated) terminal of an administrator is enough. Other users can read
status.

## Running next to the official Tailscale client

tailmix and the official client can be installed together:

- tailmix creates its own adapter ("tailmix") and never touches the official
  client's adapter, DNS rules, hosts-file entries or registry settings.
- tailmix's local addresses come from `100.127.0.0/24` by default, which is
  more specific than the official client's `100.64.0.0/10` route. If one of
  your official-client peers really uses an address in that range, choose
  another pool with `tailmixd -synthetic-pool` (see below).
- MagicDNS for tailmix's tailnets is served by tailmix's own resolver
  address; `100.100.100.100` stays with the official client.
- Use an exit node in only one of the two at a time.

## Files and logs

| What | Where |
| --- | --- |
| Programs | `C:\Program Files\tailmix` |
| Service state, node keys | `%ProgramData%\tailmix` (SYSTEM and Administrators only) |
| Service log | `%ProgramData%\tailmix\tailmixd.log` |
| Tray log | `%LOCALAPPDATA%\tailmix\tray.log` |
| DNS policy (NRPT) rule IDs | `HKLM\SOFTWARE\tailmix` |
| Control pipes | `\\.\pipe\tailmix\` |

To pass extra flags to the service, change its command line, for example:

```powershell
sc.exe config tailmixd binPath= "\"C:\Program Files\tailmix\tailmixd.exe\" -synthetic-pool 100.126.0.0/24"
Restart-Service tailmixd
```

## Uninstall

Run `uninstall.ps1` from `C:\Program Files\tailmix` (or the release folder).
Add `-Purge` to also delete `%ProgramData%\tailmix`, which logs this computer
out of every tailnet.

## Building from source

On Linux or macOS with Go, `curl`, `unzip` and `zip`:

```sh
scripts/windows/dist.sh            # dist/tailmix-windows-{amd64,arm64}.zip
```

The script downloads Wintun 0.14.1 from wintun.net and verifies its checksum.

For development on Windows, build with `GOOS=windows go build ./cmd/...` and
run `tailmixd.exe` from an elevated terminal. Without the service, put
`wintun.dll` next to `tailmixd.exe`. Tests that listen on named pipes skip
unless the test process is elevated.

## How it works on Windows

- **Adapter.** A Wintun adapter with its own GUID. It keeps Wintun's
  "Tailscale" tunnel type, which is how Tailscale's networking code recognizes
  tunnel adapters: the per-tailnet engines bind their sockets to the physical
  default interface, so tailmix's exit-node routes cannot loop.
- **Routes.** Addresses and routes are set through the IP Helper API. Exit
  nodes use two half-default routes per address family, so the physical
  default route stays in place for the engines.
- **DNS.** tailmix writes its own NRPT rules, records them under
  `HKLM\SOFTWARE\tailmix`, and sets DNS only on its own adapter. It honours
  group-policy NRPT like the official client.
- **Control.** The daemon and each tailnet's LocalAPI listen on named pipes.
  Changes are authorized from the caller's Windows token: LocalSystem and
  members of Administrators.
