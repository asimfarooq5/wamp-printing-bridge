# WAMP Printing Bridge

Transparently share printers over a WAMP router. The **host** machine exposes its CUPS printers via WAMP; the **virtual** machine auto-creates matching local CUPS queues that forward jobs back over WAMP.

## How it works

```
Virtual machine                  WAMP router              Host machine
─────────────────────            ───────────              ────────────────────
App → CUPS → wampprint ────────────────────────────────→ host receives job
             backend                                      submits to real CUPS
virtual daemon ←─── io.xconn.print.job_status ──────────
virtual daemon ──── io.xconn.printer.list ──────────────→ returns printer names
```

- **`cmd/host`** — runs on the machine with the real printer. Registers two WAMP procedures (`io.xconn.printer.list`, `io.xconn.printer.print`) and publishes job status events.
- **`cmd/virtual`** — runs on the machine that needs virtual printers. On first run it installs itself as a CUPS backend (`/usr/lib/cups/backend/wampprint`) via `pkexec`, then periodically syncs virtual CUPS queues with the remote printer list.

The `virtual` binary serves dual purpose based on how CUPS invokes it:

| Invoked as | Args | Behaviour |
|---|---|---|
| `virtual` | — | Daemon: sync queues, show job status |
| `wampprint` | 0 | CUPS device discovery |
| `wampprint` | 6 | CUPS print job → forward to host via WAMP |

## Prerequisites

- Linux with CUPS installed and running
- A WAMP router accessible from both machines (default: `ws://192.168.0.176:9090/ws`, realm `realm1`)
- Go 1.21+
- `pkexec` (for the one-time backend install on the virtual machine)

## Build

```sh
# Build both binaries
go build -o host ./cmd/host
go build -o virtual ./cmd/virtual
```

## Usage

### Host machine (real printer)

```sh
./host
```

Registers `io.xconn.printer.list` and `io.xconn.printer.print` on the WAMP router and starts forwarding print jobs to the local CUPS instance. Reconnects automatically on session loss.

### Virtual machine (virtual printers)

```sh
./virtual
```

On first run, a `pkexec` dialog will appear to install the CUPS backend (copies the binary to `/usr/lib/cups/backend/wampprint` and restarts CUPS). Subsequent runs skip this step.

Once running, the daemon:
- Polls `io.xconn.printer.list` every 10 seconds and creates/removes `Remote_<name>` CUPS queues to match
- Subscribes to `io.xconn.print.job_status` and prints job state changes to stdout

Printing from any application on the virtual machine via one of the `Remote_*` queues will transparently route the job to the host.

## Configuration

The router URL and realm are constants in each binary's `main.go`:

```
cmd/host/main.go:    routerURL = "ws://192.168.0.176:9090/ws"
cmd/virtual/main.go: routerURL = "ws://192.168.0.176:9090/ws"
```

## Tests

```sh
go test ./...
```
