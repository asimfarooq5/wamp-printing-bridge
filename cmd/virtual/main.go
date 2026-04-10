package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/xconnio/wamp_printer_bridge/internal/cups"
	"github.com/xconnio/xconn-go"
)

const (
	routerURL     = "ws://159.65.112.187:9090/ws"
	realm         = "realm1"
	backendName   = "wampprint"
	backendPath   = "/usr/lib/cups/backend/wampprint"
	debugLogPath  = "/tmp/wampprint-backend.log"
	localAPIAddr  = "127.0.0.1:17990"
	localPrintURL = "http://" + localAPIAddr + "/print"

	// virtualPPD is used when creating virtual CUPS queues on the client machine.
	// A generic PostScript PPD gives browsers (Chrome, Firefox) real printer
	// capabilities (media sizes, colour, resolution) so print-preview works, and
	// makes CUPS convert every job to PostScript before handing it to the backend.
	// The host then re-submits that PostScript through its own printer's filter
	// chain, so the correct driver on the host side handles PCL / device-specific
	// conversion.
	virtualPPD = "drv:///sample.drv/generic.ppd"

	// backendTimeout is the maximum time the CUPS backend subprocess will wait
	// for the WAMP round-trip before exiting with CUPS_BACKEND_FAILED.
	backendTimeout = 60 * time.Second
	maxBackoff     = 30 * time.Second
)

var cupsManager cups.Manager

type virtualRuntime struct {
	mu      sync.RWMutex
	session *xconn.Session
}

func (v *virtualRuntime) setSession(sess *xconn.Session) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.session = sess
}

func (v *virtualRuntime) clearSession(sess *xconn.Session) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.session == sess {
		v.session = nil
	}
}

func (v *virtualRuntime) sessionOrNil() *xconn.Session {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.session
}

// -------------------------------------------------------
// CUPS backend mode
//
// When this binary is installed as /usr/lib/cups/backend/wampprint
// and invoked by CUPS, os.Args[0] base-name is "wampprint".
// CUPS calls it in two ways:
//   - 0 args → device discovery (list devices)
//   - 6 args → job-id user title copies options file
// -------------------------------------------------------

// runCUPSDiscovery is called by CUPS with 0 args to enumerate devices.
// We output nothing because queues are created programmatically by the
// virtual daemon — there are no static devices to discover here.
func runCUPSDiscovery() {}

// runCUPSBackend reads the print job and hands it off to the long-lived local
// daemon, which is responsible for forwarding it to the host via WAMP.
// CUPS backend args: job-id user title copies options [file]
// All errors are written to stderr — CUPS records this in /var/log/cups/error_log.
func runCUPSBackend() {
	ctx, cancel := context.WithTimeout(context.Background(), backendTimeout)
	defer cancel()

	args := os.Args[1:]

	deviceURI := os.Getenv("DEVICE_URI") // e.g. wampprint:/OfficePrinter
	printer := strings.TrimPrefix(deviceURI, backendName+":/")
	printer = strings.TrimLeft(printer, "/")
	if printer == "" {
		fmt.Fprintln(os.Stderr, "ERROR: wampprint: DEVICE_URI not set or empty")
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
		fmt.Fprintf(os.Stderr, "ERROR: wampprint: read print data: %v\n", err)
		os.Exit(1)
	}

	logBackendDebug("received job printer=%s title=%q bytes=%d args=%q", printer, title, len(data), args)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, localPrintURL, bytes.NewReader(data))
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: wampprint: build local request: %v\n", err)
		os.Exit(1)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Wamp-Printer", printer)
	req.Header.Set("X-Wamp-Title", title)

	fmt.Fprintf(os.Stderr, "DEBUG: wampprint: submitting to local daemon %s (printer=%s job=%q size=%d bytes)\n",
		localPrintURL, printer, title, len(data))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		logBackendDebug("local submit failed printer=%s title=%q err=%v", printer, title, err)
		fmt.Fprintf(os.Stderr, "ERROR: wampprint: submit to local daemon %s: %v\n", localPrintURL, err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusAccepted {
		logBackendDebug("local submit rejected printer=%s title=%q status=%d body=%q", printer, title, resp.StatusCode, string(respBody))
		fmt.Fprintf(os.Stderr, "ERROR: wampprint: local daemon rejected job: status=%d body=%s\n", resp.StatusCode, strings.TrimSpace(string(respBody)))
		os.Exit(1)
	}

	logBackendDebug("job submitted printer=%s title=%q", printer, title)
	fmt.Fprintf(os.Stderr, "DEBUG: wampprint: job submitted successfully (printer=%s)\n", printer)
}

// -------------------------------------------------------
// CUPS backend installer
// -------------------------------------------------------

func ensureWampPrintBackend() error {
	needsInstall, err := backendNeedsInstall()
	if err != nil {
		return err
	}
	if !needsInstall {
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

func backendNeedsInstall() (bool, error) {
	self, err := os.Executable()
	if err != nil {
		return false, fmt.Errorf("resolve executable: %w", err)
	}

	selfDigest, err := fileSHA256(self)
	if err != nil {
		return false, fmt.Errorf("hash executable %s: %w", self, err)
	}

	backendDigest, err := fileSHA256(backendPath)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, fmt.Errorf("hash backend %s: %w", backendPath, err)
	}

	return selfDigest != backendDigest, nil
}

func fileSHA256(path string) ([32]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return [32]byte{}, err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return [32]byte{}, err
	}

	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum, nil
}

func logBackendDebug(format string, args ...any) {
	line := fmt.Sprintf("%s %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, args...))
	f, err := os.OpenFile(debugLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: wampprint: open debug log %s: %v\n", debugLogPath, err)
		return
	}
	defer f.Close()

	if _, err := f.WriteString(line); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: wampprint: write debug log %s: %v\n", debugLogPath, err)
	}
}

// -------------------------------------------------------
// Printer diff logic
// -------------------------------------------------------

// remoteEntry holds printer info received from the host.
type remoteEntry struct {
	name     string
	ppdModel string
}

func computePrinterDiff(remote []remoteEntry, local map[string]string) (toCreate []remoteEntry, toDelete []string) {
	remoteSet := make(map[string]bool, len(remote))
	for _, p := range remote {
		remoteSet[p.name] = true
	}
	for _, p := range remote {
		if _, exists := local[p.name]; !exists {
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

func fetchRemotePrinters(sess *xconn.Session) ([]remoteEntry, error) {
	resp := sess.Call("io.xconn.printer.list").Do()
	if resp.Err != nil {
		return nil, resp.Err
	}

	var remote []remoteEntry
	if resp.ArgsLen() == 0 {
		return remote, nil
	}

	arr, ok := resp.Args()[0].([]any)
	if !ok {
		return nil, fmt.Errorf("unexpected printer.list payload type %T", resp.Args()[0])
	}
	for _, v := range arr {
		entry, ok := v.(map[string]any)
		if !ok {
			continue
		}
		name, _ := entry["name"].(string)
		ppd, _ := entry["ppd"].(string)
		if name != "" {
			remote = append(remote, remoteEntry{name: name, ppdModel: ppd})
		}
	}
	return remote, nil
}

func syncPrintersOnce(ctx context.Context, sess *xconn.Session) error {
	remote, err := fetchRemotePrinters(sess)
	if err != nil {
		return fmt.Errorf("printer.list call failed: %w", err)
	}
	log.Printf("remote printers: %+v", remote)

	local, err := cupsManager.GetWampprintQueues(ctx)
	if err != nil {
		return fmt.Errorf("GetWampprintQueues failed: %w", err)
	}
	log.Printf("local wampprint queues: %+v", local)

	toCreate, toDelete := computePrinterDiff(remote, local)

	for _, p := range toCreate {
		queue := "Remote_" + p.name
		deviceURI := backendName + ":/" + p.name

		// Always use the local generic PPD on the virtual machine so desktop apps
		// see a usable printer regardless of host-side model availability.
		if err := cupsManager.CreateQueue(ctx, queue, deviceURI, virtualPPD); err != nil {
			log.Printf("CreateQueue %s (remote model=%q): %v", queue, p.ppdModel, err)
			continue
		}
		log.Printf("created queue: %s (device=%s model=%s remote=%q)", queue, deviceURI, virtualPPD, p.ppdModel)
	}

	for _, q := range toDelete {
		if err := cupsManager.DeleteQueue(ctx, q); err != nil {
			log.Printf("DeleteQueue %s: %v", q, err)
			continue
		}
		log.Printf("removed queue: %s", q)
	}

	return nil
}

func syncPrinters(ctx context.Context, sess *xconn.Session) error {
	if err := syncPrintersOnce(ctx, sess); err != nil {
		return err
	}

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := syncPrintersOnce(ctx, sess); err != nil {
				return err
			}
		}
	}
}

// -------------------------------------------------------
// Job status subscriber
// -------------------------------------------------------

func subscribeJobStatus(sess *xconn.Session) error {
	resp := sess.Subscribe("io.xconn.print.job_status", func(event *xconn.Event) {
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
	}).Do()
	return resp.Err
}

func serveLocalPrintAPI(ctx context.Context, runtime *virtualRuntime) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/print", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		printer := strings.TrimSpace(r.Header.Get("X-Wamp-Printer"))
		title := strings.TrimSpace(r.Header.Get("X-Wamp-Title"))
		if printer == "" {
			http.Error(w, "missing X-Wamp-Printer header", http.StatusBadRequest)
			return
		}

		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, fmt.Sprintf("read body: %v", err), http.StatusBadRequest)
			return
		}

		fmt.Printf("Local backend job received: printer=%s title=%q bytes=%d\n", printer, title, len(data))

		sess := runtime.sessionOrNil()
		if sess == nil {
			http.Error(w, "wamp session unavailable", http.StatusServiceUnavailable)
			return
		}

		resp := sess.Call("io.xconn.printer.print").
			Arg(printer).
			Arg(title).
			Arg(data).
			Do()
		if resp.Err != nil {
			http.Error(w, fmt.Sprintf("forward to host: %v", resp.Err), http.StatusBadGateway)
			return
		}

		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("queued"))
	})

	srv := &http.Server{
		Addr:    localAPIAddr,
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	err := srv.ListenAndServe()
	if err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// -------------------------------------------------------
// main
// -------------------------------------------------------

func main() {
	// CUPS invokes backends differently depending on the code path:
	// discovery runs the backend by filename, while print jobs set argv[0] to the
	// device URI and provide DEVICE_URI in the environment. Detect both forms.
	if isBackendInvocation() {
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	runtime := &virtualRuntime{}
	go func() {
		if err := serveLocalPrintAPI(ctx, runtime); err != nil {
			log.Printf("local print API stopped: %v", err)
			stop()
		}
	}()

	backoff := time.Second
	for {
		if ctx.Err() != nil {
			log.Println("virtual daemon shutting down")
			return
		}

		log.Printf("connecting to %s ...", routerURL)
		sess, err := xconn.ConnectAnonymous(ctx, routerURL, realm)
		if err != nil {
			log.Printf("connect: %v - retry in %v", err, backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < maxBackoff {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		runtime.setSession(sess)

		if err := subscribeJobStatus(sess); err != nil {
			log.Printf("subscribe job status: %v - reconnecting", err)
			runtime.clearSession(sess)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			continue
		}

		log.Printf("virtual_printer_host running (local api %s)...", localPrintURL)

		sessCtx, sessCancel := context.WithCancel(ctx)
		err = syncPrinters(sessCtx, sess)
		sessCancel()
		runtime.clearSession(sess)

		if err != nil && ctx.Err() == nil {
			log.Printf("session lost: %v - reconnecting in %v", err, backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < maxBackoff {
				backoff *= 2
			}
		}
	}
}

func isBackendInvocation() bool {
	if filepath.Base(os.Args[0]) == backendName {
		return true
	}

	deviceURI := os.Getenv("DEVICE_URI")
	if strings.HasPrefix(deviceURI, backendName+":") {
		return true
	}

	return strings.HasPrefix(os.Args[0], backendName+":")
}
