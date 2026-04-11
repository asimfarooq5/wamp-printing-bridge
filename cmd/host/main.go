package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/xconnio/wamp_printer_bridge/internal/cups"
	"github.com/xconnio/xconn-go"
)

const (
	routerURL         = "ws://159.65.112.187:9090/ws"
	realm             = "realm1"
	printMaxRetries   = 3
	maxBackoff        = 30 * time.Second
	heartbeatInterval = 30 * time.Second
)

// printRetryDelay is a var so tests can set it to zero.
var printRetryDelay = 2 * time.Second

var (
	session        *xconn.Session
	cupsManager    cups.Manager
	hostRuntimeCtx context.Context
)

// JobStatus carries state for a single print job published over WAMP.
type JobStatus struct {
	JobID     string
	Printer   string
	State     string // queued | retrying | printed | failed
	Message   string
	CreatedAt int64
}

// publishJobStatus broadcasts a job event as four positional args so the
// virtual host subscriber can read them individually.
func publishJobStatus(js JobStatus) {
	if session == nil {
		return
	}
	resp := session.Publish("io.xconn.print.job_status").
		Arg(js.JobID).
		Arg(js.Printer).
		Arg(js.State).
		Arg(js.Message).
		Do()
	if resp.Err != nil {
		log.Printf("publishJobStatus: %v", resp.Err)
	}
}

// listPrinters is exposed as RPC: "io.xconn.printer.list"
// Returns a list of {name, ppd} maps so the virtual side can create matching queues.
func listPrinters(ctx context.Context, inv *xconn.Invocation) *xconn.InvocationResult {
	infos, err := cupsManager.GetPrintersInfo(ctx)
	if err != nil {
		log.Printf("listPrinters: %v", err)
		infos = []cups.PrinterInfo{}
	}
	result := make([]map[string]any, len(infos))
	for i, info := range infos {
		result[i] = map[string]any{"name": info.Name, "ppd": info.PPDModel}
	}
	res := xconn.NewInvocationResult()
	res.Args = []any{result}
	return res
}

// executePrint writes data to a unique temp file, submits it to CUPS with
// up to printMaxRetries attempts, and publishes job status at every stage.
func executePrint(ctx context.Context, jobID, printer, filename string, data []byte) {
	tmp, err := os.CreateTemp("", "wampprint-*-"+filename)
	if err != nil {
		log.Printf("[%s] CreateTemp: %v", jobID, err)
		publishJobStatus(JobStatus{
			JobID: jobID, Printer: printer, State: "failed",
			Message: "could not create temp file", CreatedAt: time.Now().Unix(),
		})
		return
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		log.Printf("[%s] WriteFile: %v", jobID, err)
		publishJobStatus(JobStatus{
			JobID: jobID, Printer: printer, State: "failed",
			Message: "could not write temp file", CreatedAt: time.Now().Unix(),
		})
		return
	}

	publishJobStatus(JobStatus{
		JobID: jobID, Printer: printer, State: "queued", CreatedAt: time.Now().Unix(),
	})

	var lastErr error
	for attempt := 1; attempt <= printMaxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			lastErr = err
			break
		}

		var cupsJobID int
		cupsJobID, lastErr = cupsManager.PrintRaw(ctx, printer, tmpPath)
		if lastErr == nil {
			log.Printf("[%s] CUPS job %d on %s", jobID, cupsJobID, printer)
			publishJobStatus(JobStatus{
				JobID: jobID, Printer: printer, State: "printed", CreatedAt: time.Now().Unix(),
			})
			return
		}

		log.Printf("[%s] attempt %d/%d failed: %v", jobID, attempt, printMaxRetries, lastErr)

		if attempt < printMaxRetries {
			publishJobStatus(JobStatus{
				JobID:     jobID,
				Printer:   printer,
				State:     "retrying",
				Message:   fmt.Sprintf("attempt %d: %v", attempt, lastErr),
				CreatedAt: time.Now().Unix(),
			})
			select {
			case <-ctx.Done():
				return
			case <-time.After(printRetryDelay):
			}
		}
	}

	log.Printf("[%s] all %d attempts failed on %s", jobID, printMaxRetries, printer)
	publishJobStatus(JobStatus{
		JobID: jobID, Printer: printer, State: "failed",
		Message: lastErr.Error(), CreatedAt: time.Now().Unix(),
	})
}

func sendPrint(ctx context.Context, inv *xconn.Invocation) *xconn.InvocationResult {
	printer, _ := inv.ArgString(0)
	filename, _ := inv.ArgString(1)
	data, _ := inv.ArgBytes(2)

	jobID := fmt.Sprintf("%s-%d", printer, time.Now().UnixNano())

	if printer == "" {
		log.Printf("sendPrint: empty printer name")
		publishJobStatus(JobStatus{
			JobID: jobID, Printer: printer, State: "failed",
			Message: "empty printer name", CreatedAt: time.Now().Unix(),
		})
		return xconn.NewInvocationResult()
	}

	// Do not tie the actual CUPS submission to the RPC invocation lifecycle.
	// The caller only needs an acknowledgement that the job was accepted; the
	// print itself should continue until the host process shuts down.
	printCtx := hostRuntimeCtx
	if printCtx == nil {
		printCtx = context.Background()
	}

	go executePrint(printCtx, jobID, printer, filename, data)

	return xconn.NewInvocationResult()
}

func healthCheck(ctx context.Context, sess *xconn.Session, cancel context.CancelFunc) {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if resp := sess.Publish("io.xconn.print.host_heartbeat").Do(); resp.Err != nil {
				log.Printf("heartbeat failed: %v — reconnecting", resp.Err)
				cancel()
				return
			}
		}
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	hostRuntimeCtx = ctx

	cupsManager = cups.NewClient("localhost", 631)

	backoff := time.Second
	for {
		log.Printf("connecting to %s ...", routerURL)
		sess, err := xconn.ConnectAnonymous(ctx, routerURL, realm)
		if err != nil {
			log.Printf("connect: %v — retry in %v", err, backoff)
			select {
			case <-ctx.Done():
				log.Println("shutting down")
				return
			case <-time.After(backoff):
			}
			if backoff < maxBackoff {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second

		sessCtx, sessCancel := context.WithCancel(ctx)
		session = sess

		if resp := sess.Register("io.xconn.printer.list", listPrinters).Do(); resp.Err != nil {
			log.Printf("register listPrinters: %v — reconnecting", resp.Err)
			sessCancel()
			session = nil
			continue
		}
		if resp := sess.Register("io.xconn.printer.print", sendPrint).Do(); resp.Err != nil {
			log.Printf("register sendPrint: %v — reconnecting", resp.Err)
			sessCancel()
			session = nil
			continue
		}

		go healthCheck(sessCtx, sess, sessCancel)

		log.Printf("printer host ready (procedures: io.xconn.printer.list, io.xconn.printer.print)")

		select {
		case <-ctx.Done():
			sessCancel()
			session = nil
			log.Println("shutting down")
			return
		case <-sessCtx.Done():
			session = nil
			if ctx.Err() == nil {
				log.Printf("session lost — reconnecting in %v", backoff)
			}
		}
	}
}
