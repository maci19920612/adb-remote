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

The transporter reads `config.json` from its current working directory:

```json
{ "transporterAddress": "0.0.0.0:9000" }
```

`transporterAddress` here is the **listen** address.

```sh
cd transporter
cp config.example.json config.json   # edit as needed
go run .
# or: ./transporter   (after `go build`)
```

### Running the transporter with Docker/Podman

```sh
# from the repository root — the build context must be the workspace root,
# since the image needs the shared/ and client/ modules too
podman build -f transporter/Dockerfile -t adb-remote-transporter .

podman run -d --name adb-remote-transporter \
  -p 9000:9000 \
  adb-remote-transporter
```

The image bakes in `transporter/config.example.json` (`0.0.0.0:9000`) as
`/app/config.json`. To use your own:

```sh
podman run -d --name adb-remote-transporter \
  -p 9000:9000 \
  -v "$(pwd)/my-config.json:/app/config.json:ro" \
  adb-remote-transporter
```

## Running the client

The client also reads `config.json` from its current working directory:

```json
{ "transporterAddress": "127.0.0.1:9000" }
```

Here `transporterAddress` is the address of the (remote) transporter to
**dial**.

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
Your client id: MVJK1457
Room id:        QNZQ5630

Waiting for guests to join...
```

Share the room id out of band (chat, etc.) with whoever you want to give
access to. Every join request shows the guest's client id and prompts
`[y/n]`:

```
Join request from clientId: QLHW5807 — accept? [y/n]
```

Accepted/declined requests are logged with the client id in the activity
feed below. Pass `--yes` to auto-accept every request instead of prompting
(useful for scripting/demos, not recommended for anything you didn't set up
yourself) — accept/decline still gets logged with the client id either way.

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
Room id:        QNZQ5630

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
free local port.

Client logs (from the underlying transport/relay layers) don't go to
stdout — that's reserved for the TUI — they're written to
`$TMPDIR/adb-remote-client.log` instead.

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
