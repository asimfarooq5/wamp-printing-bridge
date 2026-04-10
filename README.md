# WAMP Printing Bridge

Share printers over a WAMP router. The `host` machine exposes its local CUPS printers via WAMP, and the `virtual` machine creates matching local CUPS queues that forward print jobs back to the host.

The current end-to-end flow is working:
- `virtual` can discover printers from `host`
- `virtual` creates local `Remote_*` CUPS queues
- printing to a `Remote_*` queue sends the job through WAMP to `host`
- `host` submits the job to the real local CUPS printer

## How it works

```
Virtual machine                  WAMP router              Host machine
─────────────────────            ───────────              ────────────────────
App -> CUPS -> wampprint backend -> local virtual daemon -> io.xconn.printer.print
                                                        \
                                                         -> host receives job -> local CUPS -> real printer

virtual daemon <- io.xconn.print.job_status
virtual daemon -> io.xconn.printer.list -> host returns printers
```

## Components

- `cmd/host`
  Runs on the machine with the real printers. Connects to the WAMP router, registers `io.xconn.printer.list` and `io.xconn.printer.print`, and submits incoming jobs to the local CUPS server.
- `cmd/virtual`
  Runs on the client machine as a long-lived daemon. Connects to the WAMP router, syncs local `Remote_*` queues from the host printer list, installs the `wampprint` CUPS backend on first run, and forwards jobs received from CUPS to the host over WAMP.
- `internal/cups`
  Shared CUPS adapter used by both binaries.

The `virtual` binary serves dual purpose based on how CUPS invokes it:

| Invoked as | Args | Behaviour |
|---|---|---|
| `virtual` | — | Daemon: sync queues, show job status |
| `wampprint` | 0 | CUPS device discovery |
| `wampprint` | 6 | CUPS print job -> hand off to local daemon -> forward to host via WAMP |

## Prerequisites

- Linux with CUPS installed and running
- A WAMP router reachable from both machines
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

Behavior:
- connects to the configured WAMP router
- registers `io.xconn.printer.list`
- registers `io.xconn.printer.print`
- publishes `io.xconn.print.job_status`
- reconnects automatically on session loss

Important: the real CUPS printer on the host must be enabled and accepting jobs.

Useful host-side checks:

```sh
lpstat -p -a
```

If a printer shows `does not accept jobs`, fix it on the host:

```sh
cupsenable <printer_name>
cupsaccept <printer_name>
```

### Virtual machine (virtual printers)

```sh
./virtual
```

On first run, a `pkexec` dialog installs the CUPS backend by copying the binary to `/usr/lib/cups/backend/wampprint` and restarting CUPS. Later runs skip installation unless the binary changed.

Once running, the daemon:
- keeps a local HTTP endpoint at `127.0.0.1:17990/print` for the CUPS backend handoff
- reconnects automatically to the WAMP router if the session drops
- calls `io.xconn.printer.list` every 10 seconds
- creates missing `Remote_<name>` queues and removes stale ones
- subscribes to `io.xconn.print.job_status` and prints job updates to stdout
- creates queues with backend device URIs in the `wampprint:/PrinterName` form

Printing from any application on the virtual machine through a `Remote_*` queue follows this path:

1. the application prints to the local CUPS queue
2. CUPS invokes the `wampprint` backend
3. the backend posts the job to the local virtual daemon
4. the daemon calls `io.xconn.printer.print`
5. the host receives the job and submits it to its local CUPS printer

## Configuration

The router URL and realm are currently hardcoded constants in each binary:

```
cmd/host/main.go
cmd/virtual/main.go
```

At the moment both binaries must point to the same WAMP router and realm.

## Current WAMP API

- Procedure: `io.xconn.printer.list`
  Returns printer info from the host
- Procedure: `io.xconn.printer.print`
  Accepts a print job from the client
- Topic: `io.xconn.print.job_status`
  Publishes host-side job state updates

## Testing Notes

Basic local test suite:

```sh
go test ./...
```

Manual end-to-end validation:

1. start the WAMP router
2. run `./host` on the printer machine
3. run `./virtual` on the client machine
4. confirm `Remote_*` printers appear in CUPS on the client
5. print a small PDF to one of the `Remote_*` printers
6. confirm the job appears on the host printer and prints physically

## Current Limitations

- printer presence and job forwarding are working
- richer host printer state publication is still limited
- router URL and realm are not configurable yet
- service units for persistent deployment are not included yet
