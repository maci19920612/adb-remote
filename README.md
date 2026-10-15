# adb-remote

Share a local ADB device (physical device or emulator) with someone else over
the network, and let them drive it with their own `adb`, without exposing
raw ADB ports directly.

Three Go modules, tied together by the `go.work` workspace at the repo root:

- **`shared`** — the wire protocol between clients and the transporter, and a
  couple of shared utilities (e.g. the pooled/disposable object pool used to
  avoid allocating a new message buffer per packet).
- **`transporter`** — a relay server. It never looks at ADB traffic; it just
  brokers **rooms**: one client creates a room (the *owner*, who has the
  device), a second client joins it (the *guest*), and once the owner
  accepts the join request the transporter blindly forwards
  `CommandAdbTransport` messages between the two.
- **`client`** — the CLI, with an interactive terminal UI (Bubble Tea) for
  both modes:
  - `share`: owns a device (as seen by its local `adb devices`) and offers
    it to a room — pick the device from a live list (with refresh), then
    watch join requests arrive and accept/decline them.
  - `connect`: joins a room and exposes the shared device locally, so a real
    `adb connect 127.0.0.1:<port>` on that machine can drive it — shows your
    client id and the connection state as it progresses.

## Building

Each module is built independently, but `go.work` lets you build/test from
anywhere in the tree without a `replace` directive:

```sh
cd transporter && go build -o transporter .
cd client      && go build -o adb-remote-client .
```

## Running the transporter

The transporter reads `config.json` from its current working directory.
`transporter/config.example.json` lists every setting with its default
value, so it can be copied as is and trimmed down to `transporterAddress`
without changing anything:

```json
{
  "transporterAddress": "0.0.0.0:9000",
  "tlsCertFile": "transporter-cert.pem",
  "tlsKeyFile": "transporter-key.pem",
  "verbosity": "default",
  "pcapFile": "transporter.pcapng",
  "maxConnections": 1024,
  "maxConnectionsPerIp": 32,
  "maxRooms": 512,
  "maxRoomsPerIp": 8,
  "roomIdleTimeout": "24h"
}
```

| Setting | Default | Meaning |
|---|---|---|
| `transporterAddress` | required | The address to **listen** on. |
| `tlsCertFile`, `tlsKeyFile` | `transporter-cert.pem`, `transporter-key.pem` | The TLS certificate and key, generated on the first start (see below). Relative paths are relative to the working directory. |
| `verbosity` | `"default"` | `"debug"` turns on debug logging and the packet capture (see [Debug packet capture](#debug-packet-capture)). |
| `pcapFile` | `transporter.pcapng` | Where the debug packet capture goes; only used with `"verbosity": "debug"`. |
| `maxConnections` | `1024` | Concurrent client connections in total. |
| `maxConnectionsPerIp` | `32` | Concurrent client connections from one address. |
| `maxRooms` | `512` | Rooms in total. |
| `maxRoomsPerIp` | `8` | Rooms owned from one address. |
| `roomIdleTimeout` | `"24h"` | A Go duration (`"90m"`, `"24h"`): a room that goes this long without an accepted guest is closed. `"0"` disables it. |

A limit left at `0` (or out) uses its default; a negative limit, an unknown
`verbosity` or an unparseable `roomIdleTimeout` stops the transporter at
startup. On top of the limits, a client gets 10 seconds for the TLS
handshake and the protocol handshake, 60 seconds to finish a message once
it started sending one, and 30 seconds for every write the transporter
makes to it; one that stops reading is disconnected rather than allowed to
hold up anyone else. An address that asks for 10 room ids that don't exist
within a minute is refused further join requests until the minute is up.

```sh
cd transporter
cp config.example.json config.json   # edit as needed
go run .
# or: ./transporter   (after `go build`)
```

On the first start the transporter generates a self-signed TLS certificate
(`transporter-cert.pem`/`transporter-key.pem`, or `tlsCertFile`/`tlsKeyFile`)
and keeps using it afterwards. Every start prints its fingerprint, ready to
be pinned in the clients' configuration (see
[Certificate pinning](#certificate-pinning)):

```
INFO: Loaded the TLS certificate from transporter-cert.pem
INFO: Transporter TLS certificate fingerprint: SHA256:Uq3ld0dKK7ytd0IA3YdeS2xAkTwpsUU4i3Se27ip8sw
INFO: To pin it, set "transporterCertificateFingerprint": "SHA256:Uq3ld0dKK7ytd0IA3YdeS2xAkTwpsUU4i3Se27ip8sw" in the clients' config.json
```

Deleting either file makes the transporter generate a new certificate (it
says so on startup), and clients that pinned the old one refuse to connect
until they are given the new fingerprint.

### Debug packet capture

Set `"verbosity": "debug"` (default: `"default"`) to turn on debug logging
and record every byte of every connection to a pcapng file:

```json
{
  "transporterAddress": "0.0.0.0:9000",
  "verbosity": "debug",
  "pcapFile": "transporter.pcapng"
}
```

`pcapFile` is optional and defaults to `transporter.pcapng` in the working
directory; the file is overwritten on every start. The capture is taken
below TLS, so it also shows connections whose TLS handshake failed and
clients that never spoke TLS at all. Each connection appears as its own TCP
stream between the real client and transporter addresses, and the TLS
session secrets are embedded in the file, so Wireshark decrypts the
successful sessions on open — no key log setup needed.

**The capture holds all relayed traffic and the keys to decrypt it.** It is
created with mode `0600`; treat it as sensitive and only enable debug
verbosity while debugging.

### Running the transporter with Docker/Podman

```sh
# from the repository root — the build context must be the workspace root,
# since the image needs the shared/ and client/ modules too
podman build -f transporter/Dockerfile -t adb-remote-transporter .

podman run -d --name adb-remote-transporter \
  -p 9000:9000 \
  adb-remote-transporter
```

The image bakes in `transporter/config.docker.json` as `/app/config.json`:
it listens on `0.0.0.0:9000` and keeps the TLS certificate and key in
`/data`, the only directory the (non-root) service user can write to. Mount
a volume there so the transporter keeps the same TLS identity across
containers:

```sh
podman run -d --name adb-remote-transporter \
  -p 9000:9000 \
  -v adb-remote-transporter-data:/data \
  adb-remote-transporter
```

To use your own configuration, mount it over `/app/config.json` and keep
`tlsCertFile`/`tlsKeyFile` pointing into `/data`:

```sh
podman run -d --name adb-remote-transporter \
  -p 9000:9000 \
  -v adb-remote-transporter-data:/data \
  -v "$(pwd)/my-config.json:/app/config.json:ro" \
  adb-remote-transporter
```

For debug packet capture in a container, point `pcapFile` at a mounted,
writable directory (e.g. `"pcapFile": "/captures/transporter.pcapng"` with
`-v "$(pwd)/captures:/captures"`), so the file outlives the container.

## Running the client

The client also reads `config.json` from its current working directory;
`client/config.example.json` lists every setting:

```json
{
  "transporterAddress": "127.0.0.1:9000",
  "transporterCertificateFingerprint": ""
}
```

| Setting | Default | Meaning |
|---|---|---|
| `transporterAddress` | required | The address of the (remote) transporter to **dial**. |
| `transporterCertificateFingerprint` | `""` (no pinning) | The transporter certificate's fingerprint to pin (see [Certificate pinning](#certificate-pinning)). |

### Certificate pinning

Optionally, pin the transporter's TLS certificate with the fingerprint the
transporter prints on startup:

```json
{
  "transporterAddress": "transporter.example.com:9000",
  "transporterCertificateFingerprint": "SHA256:Uq3ld0dKK7ytd0IA3YdeS2xAkTwpsUU4i3Se27ip8sw"
}
```

The client then refuses to connect to anything presenting another
certificate, e.g. someone intercepting the connection, and says which
fingerprint it got instead. Without it, the client accepts whatever
certificate the transporter presents. A malformed fingerprint is an error
at startup rather than silently ignored.

Both commands launch an interactive terminal UI and need a real terminal
(they exit with an error if stdout isn't a TTY — no surprise garbled output
in a script or pipe).

### Owner: sharing a device

```sh
cd client
cp config.example.json config.json   # point at your transporter
go run . share
```

You'll see a live device list (from your local `adb devices`); press `r` to
refresh it, arrow keys to move, `enter` to pick one. Pass `--targetDevice
emulator-5554` to skip the picker and go straight to creating a room. Once
the room exists you'll see:

```
Your client id:   MVJK1457
Your fingerprint: SHA256:x8Hq...
Room id:          7KQ2-M9XD-4HTB

Waiting for guests to join...
```

Share the room id out of band (chat, etc.) with whoever you want to give
access to. Every join request shows the guest's client id and identity
fingerprint and prompts `[y/n]`:

```
Join request from clientId: QLHW5807 — accept? [y/n]
  Guest fingerprint: SHA256:p0Vn...
```

Compare that fingerprint with the one the guest's own screen shows (over a
call, chat, in person) before accepting: the session is encrypted end to end
to that key (see [Security](#security)), so a matching fingerprint means
nobody in between can read or change the traffic. The guest's screen
likewise shows yours to compare.

Accepted/declined requests are logged with the client id in the activity
feed below. Pass `--trust SHA256:...,SHA256:...` to accept the guests with
those fingerprints automatically while still being asked about anyone else.
Pass `--yes` to auto-accept every request instead of prompting (anyone who
learns the room id gets the device; useful for scripting/demos, not
recommended for anything you didn't set up yourself) — accept/decline still
gets logged with the client id either way.

### Guest: connecting to a shared device

```sh
cd client
go run . connect --targetRoomId <ROOM_ID> --port 5038
```

This shows your client id and the connection state as it progresses
(`connecting to the transporter...` → `joining room...` →
`ready — waiting for a local adb connection` → `relaying ADB traffic`, or
an error/declined state). You don't need to run `adb connect` yourself: as
soon as the local proxy is up, it runs the equivalent of `adb connect
127.0.0.1:<port>` automatically (via the same smartsocket protocol the real
`adb` CLI uses — no external process involved), so your local `adb devices`
picks up the shared device on its own:

```
Your client id: QLHW5807
Room id:        7KQ2-M9XD-4HTB

Connection state: relaying ADB traffic

Local proxy: 127.0.0.1:5038
adb connect issued automatically

(1 local adb connection(s) relayed so far)
```

```sh
adb -s 127.0.0.1:5038 shell
```

If the automatic connect fails for some reason, the TUI shows the error and
the command to run yourself as a fallback. Either way, `adb disconnect
127.0.0.1:<port>` runs automatically when you quit (`q`) or the process
exits for any other reason, so a stale entry doesn't linger in `adb
devices`.

`--port` defaults to `5038` (`adb.DefaultProxyPort`) and just needs to be a
free local port. The proxy only listens on `127.0.0.1`: it authenticates
nobody, so it must not be reachable from the network.

Client logs (from the underlying transport/relay layers) don't go to
stdout — that's reserved for the TUI — they're written to
`$TMPDIR/adb-remote-client.log` instead.

## Security

- **ADB traffic is encrypted end to end** between the owner and the guest
  (`client/e2e`). The guest's join request and the owner's acceptance carry
  an X25519 key exchange, each half signed with that side's identity key
  (`~/.adb-remote/identity`, Ed25519), the key whose fingerprint the TUI
  shows. Each direction then uses its own AES-256-GCM key, with a message
  counter as the nonce, so a message that is altered, dropped, repeated or
  reordered ends the session. The transporter only routes the encrypted
  messages; it still sees who talks to whom, room ids, and how much and when
  they send. A relay that swapped the key exchange for its own couldn't sign
  it with the genuine identity keys, and swapping the identity keys as well
  changes the fingerprints the two people compare.
- **The client-transporter connection is TLS 1.3**, against the
  transporter's self-signed certificate. A client that pins the
  certificate's fingerprint ([Certificate pinning](#certificate-pinning))
  connects only to that transporter; without pinning, TLS only keeps
  passive listeners out. Either way, the end-to-end encryption above is
  what protects the ADB traffic.
- **The guest only gets the shared device.** The owner opens just the device
  services a real adb client asks a device for (`shell`, `sync:`, `exec:`,
  forwarded sockets, …) and refuses everything its adb-server would treat
  as a request to itself (`host:kill`, `host:connect:…`, switching to
  another device, malformed `reverse:` requests).
- **Nothing flows before the owner accepts.** The transporter relays ADB
  traffic only once the owner accepted the room's guest, the owner's
  decision names the guest it is about, and a declined guest is
  disconnected.
- **The owner can't crash the guest's adb-server.** The guest only lets the
  owner open the reverse-forward targets its own adb-server asked for
  (anything else makes adb-server abort), and drops `CNXN`/`SYNC` from the
  owner.
- **Reverse-forward relay endpoints are private:** on macOS and Linux the
  device's connections reach the owner through a Unix socket in a directory
  only the owner's user can enter (a loopback TCP port on Windows).
- **Room ids** are 12 random base32 symbols (60 bits), and the transporter
  rate-limits failed join attempts per address.

Protocol version 2 introduced the end-to-end encryption; version 1 clients
and transporters can't talk to version 2 ones.

## Testing

Every package has unit and/or integration tests; the protocol, pool, relay
and room-lifecycle tests spin up real listeners/pipes rather than mocking
the network. The TUI models (`client/tui`) are tested by calling `Update`
directly with synthetic messages — no real terminal needed:

```sh
cd shared      && go test ./... -race
cd client      && go test ./... -race
cd transporter && go test ./... -race
```

## Status

Both roles are verified end-to-end against a real `adb` client
(platform-tools 35.0.0) and a real Android emulator: create a room, join
it, `adb connect`, `adb devices` shows the shared device as `device`
(online), and real traffic flows correctly in both directions — `adb
shell` (including exit codes), `adb push`/`pull` of a 512KB file (exercises
the `sync:` protocol and multi-chunk flow control), and several concurrent
`adb shell` commands relayed as independent streams at once.

- **Room lifecycle** (connect, create room, join room, accept/decline) and
  the **transporter's message relay**: verified with integration tests and
  live traffic — see the `connectionManager`/`roomManager` tests and
  `client/controller`'s tests.
- **Guest role (`connect`)**: a local `AdbProxy` (`client/adb/proxy.go`)
  performs the CNXN handshake with the local `adb` server and hands the
  connection to `client/relay.Run`, which pumps ADB messages 1:1 between it
  and the transporter. This is the easy direction, since a raw ADB
  wire-protocol connection is exactly what a real `adb connect`-ed device
  looks like.
- **Owner role (`share`)**: real `adb-server` does **not** expose a raw ADB
  wire-protocol (CNXN/OPEN/WRTE/OKAY/CLSE) pass-through to a device over its
  public API — `host:transport:<serial>` merely selects a device; the next
  thing it expects is a *second smartsocket-style service request* (e.g.
  `shell,v2,raw:<cmd>`, `sync:`), verified directly against `adb-server`
  with a raw socket, independent of this codebase. So the owner side
  (`client/relay.OwnerMultiplexer`, in `client/relay/owner.go`) is a
  per-stream multiplexer: for every `OPEN` the guest sends, it dials a
  fresh `host:transport:<serial>` + service-string connection to the local
  adb-server and relays just that one stream as `WRTE`/`OKAY`/`CLSE`,
  honoring basic ADB flow control (wait for an `OKAY` after each `WRTE`
  before sending the next one for that stream). Deciding whether to accept
  a join request (the TUI's y/n prompt, or `--yes`) runs off the main
  dispatch loop so it can't stall an already-connected guest's traffic —
  see `TestJoinRequestDoesNotBlockActiveStreamTraffic`.
- **Reverse forwards** (`adb reverse`, which scrcpy and Android Studio's
  device mirroring use to have the device connect back to the host):
  verified with scrcpy 3.3.3 against a real device, with the owner's
  adb-server sandboxed so it can't reach the guest's listeners, as when
  owner and guest are two machines. The owner can't pass
  `reverse:forward:<remote>;<local>` through unchanged, since its own
  adb-server would then connect to `<local>` on the owner's machine. So
  `client/relay/reverse.go` points the device at an owner-side relay
  endpoint instead (a private Unix socket, `localfilesystem:<path>`, except
  on Windows) and hands each connection arriving there to the guest as a
  device-initiated `OPEN` for the original `<local>`, which the guest's
  adb-server connects to itself.
- **The TUI itself**: verified with a real two-process, real-terminal (PTY)
  run — device list → refresh → select → room id, a real guest joining
  with its client id shown and accepted, and the guest side showing its
  client id and connection state all the way to "relaying ADB traffic" —
  including the automatic `adb connect`/`adb disconnect`, confirmed against
  `adb devices` before, during, and after the run (no stale entries left
  behind on quit). Model state transitions are additionally unit-tested
  directly (`client/tui`).

### Two ADB wire-format quirks that will break this against real `adb` if regressed

- **The "checksum" isn't a CRC32.** The ADB message header's `data_check`
  field is the sum of the payload bytes truncated to 32 bits, not a real
  CRC32 despite the name — and every message after the initial `CNXN`
  carries a literal `0` there, unvalidated by real adb-server/adbd. This
  code doesn't validate it either (see `client/adb/adbMessage.go` and its
  tests, including one built from a real captured `adb connect` packet).
- **The CNXN device banner must advertise `features=shell_v2,...`.**
  Without it, a real `adb` client silently negotiates the legacy v1
  `shell:` service instead of `shell,v2,raw:`, which relays stdout/stderr
  fine but has no way to carry the remote command's exit code back at all
  (`adb shell "exit 7"` would report `0` instead of `7`, with no error).
  See `deviceFeatures` in `client/adb/proxy.go`.
