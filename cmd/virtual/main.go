package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/xconnio/wamp_printer_bridge/internal/cups"
	"github.com/xconnio/xconn-go"
)

const (
	routerURL   = "ws://192.168.0.176:9090/ws"
	realm       = "realm1"
	backendName = "wampprint"
	backendPath = "/usr/lib/cups/backend/wampprint"
)

var cupsManager cups.Manager

// -------------------------------------------------------
// CUPS backend mode
//
// When this binary is installed as /usr/lib/cups/backend/wampprint
// and invoked by CUPS, os.Args[0] base-name is "wampprint".
// CUPS calls it in two ways:
//   - 0 args → device discovery (list devices)
//   - 6 args → job-id user title copies options file
// -------------------------------------------------------

func runCUPSDiscovery() {
	fmt.Printf("direct %s \"WAMP Print Bridge\" \"WAMP virtual printer backend\"\n", backendName)
}

// runCUPSBackend reads the print job and forwards it to the host via WAMP.
// CUPS backend args: job-id user title copies options [file]
func runCUPSBackend() {
	args := os.Args[1:]

	deviceURI := os.Getenv("DEVICE_URI") // e.g. wampprint://OfficePrinter
	printer := strings.TrimPrefix(deviceURI, backendName+"://")
	if printer == "" {
		fmt.Fprintln(os.Stderr, "DEVICE_URI not set or empty")
		os.Exit(1)
	}

	title := args[2]

	var data []byte
	var err error
	if len(args) >= 6 && args[5] != "" {
		data, err = os.ReadFile(args[5])
	} else {
		data, err = io.ReadAll(os.Stdin)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "read print data:", err)
		os.Exit(1)
	}

	ctx := context.Background()
	sess, err := xconn.ConnectAnonymous(ctx, routerURL, realm)
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect:", err)
		os.Exit(1)
	}

	resp := sess.Call("io.xconn.printer.print").
		Arg(printer).
		Arg(title).
		Arg(data).
		Do()
	if resp.Err != nil {
		fmt.Fprintln(os.Stderr, "print:", resp.Err)
		os.Exit(1)
	}
}

// -------------------------------------------------------
// CUPS backend installer
// -------------------------------------------------------

func ensureWampPrintBackend() error {
	if _, err := os.Stat(backendPath); err == nil {
		fmt.Println("WAMP backend already installed")
		return nil
	}

	fmt.Println("Installing CUPS WAMP backend via pkexec...")

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}

	cmd := exec.Command("pkexec", "install", "-m", "755", self, backendPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pkexec install failed: %w", err)
	}

	if err := exec.Command("pkexec", "systemctl", "restart", "cups").Run(); err != nil {
		return fmt.Errorf("restart cups: %w", err)
	}

	fmt.Println("WAMP CUPS backend installed & cups restarted")
	return nil
}

// -------------------------------------------------------
// Printer diff logic
// -------------------------------------------------------

func computePrinterDiff(remote []string, local map[string]string) (toCreate []string, toDelete []string) {
	remoteSet := make(map[string]bool, len(remote))
	for _, p := range remote {
		remoteSet[p] = true
	}
	for _, p := range remote {
		if _, exists := local[p]; !exists {
			toCreate = append(toCreate, p)
		}
	}
	for p, q := range local {
		if !remoteSet[p] {
			toDelete = append(toDelete, q)
		}
	}
	return
}

// -------------------------------------------------------
// Sync loop
// -------------------------------------------------------

func syncPrinters(ctx context.Context, sess *xconn.Session) {
	for {
		time.Sleep(10 * time.Second)

		resp := sess.Call("io.xconn.printer.list").Do()
		if resp.Err != nil {
			fmt.Println("print.list call failed:", resp.Err)
			continue
		}

		var remote []string
		if resp.ArgsLen() > 0 {
			if arr, ok := resp.Args()[0].([]any); ok {
				for _, v := range arr {
					if s, ok := v.(string); ok {
						remote = append(remote, s)
					}
				}
			}
		}
		fmt.Println("Remote printers:", remote)

		local, err := cupsManager.GetWampprintQueues(ctx)
		if err != nil {
			fmt.Println("GetWampprintQueues failed:", err)
			continue
		}
		fmt.Println("Local wampprint queues:", local)

		toCreate, toDelete := computePrinterDiff(remote, local)

		for _, p := range toCreate {
			queue := "Remote_" + p
			if err := cupsManager.CreateQueue(ctx, queue, backendName+"://"+p); err != nil {
				fmt.Printf("CreateQueue %s: %v\n", queue, err)
			} else {
				fmt.Println("Created queue:", queue)
			}
		}
		for _, q := range toDelete {
			if err := cupsManager.DeleteQueue(ctx, q); err != nil {
				fmt.Printf("DeleteQueue %s: %v\n", q, err)
			} else {
				fmt.Println("Removed queue:", q)
			}
		}
	}
}

// -------------------------------------------------------
// Job status subscriber
// -------------------------------------------------------

func subscribeJobStatus(sess *xconn.Session) {
	sess.Subscribe("io.xconn.print.job_status", func(event *xconn.Event) {
		if event.ArgsLen() < 3 {
			return
		}
		args := event.Args()
		jobID, _ := args[0].(string)
		printer, _ := args[1].(string)
		state, _ := args[2].(string)
		message := ""
		if len(args) > 3 {
			message, _ = args[3].(string)
		}
		if message != "" {
			fmt.Printf("[JOB %s] %s -> %s (%s)\n", jobID, printer, state, message)
		} else {
			fmt.Printf("[JOB %s] %s -> %s\n", jobID, printer, state)
		}
	})
}

// -------------------------------------------------------
// main
// -------------------------------------------------------

func main() {
	// When CUPS installs and invokes this binary as "wampprint", run in backend mode.
	if filepath.Base(os.Args[0]) == backendName {
		if len(os.Args) == 1 {
			runCUPSDiscovery()
		} else {
			runCUPSBackend()
		}
		return
	}

	// Daemon mode
	if err := ensureWampPrintBackend(); err != nil {
		panic(err)
	}

	cupsManager = cups.NewClient("localhost", 631)

	ctx := context.Background()

	sess, err := xconn.ConnectAnonymous(ctx, routerURL, realm)
	if err != nil {
		panic(err)
	}

	subscribeJobStatus(sess)
	go syncPrinters(ctx, sess)

	fmt.Println("virtual_printer_host running...")
	select {}
}
