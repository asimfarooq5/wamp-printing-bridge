package cups

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	ipp "github.com/phin1x/go-ipp"
)

const ippStatusNoDestinations = int16(1030)

type PrinterInfo struct {
	Name     string
	PPDModel string
}

type Manager interface {
	GetPrintersInfo(ctx context.Context) ([]PrinterInfo, error)

	PrintRaw(ctx context.Context, printer, filePath string) (int, error)

	GetWampPrintQueues(ctx context.Context) (map[string]string, error)

	CreateQueue(ctx context.Context, name, deviceURI, ppdModel string) error

	DeleteQueue(ctx context.Context, name string) error
}

type Client struct {
	host string
	port int
}

func NewClient(host string, port int) *Client {
	return &Client{host: host, port: port}
}

func (c *Client) cupsClient() *ipp.CUPSClient {
	return ipp.NewCUPSClientWithAdapter("", ipp.NewSocketAdapter(c.host, false))
}

func (c *Client) ippClient() *ipp.IPPClient {
	return ipp.NewIPPClientWithAdapter("", ipp.NewSocketAdapter(c.host, false))
}

func (c *Client) GetPrintersInfo(ctx context.Context) ([]PrinterInfo, error) {
	printers, err := c.cupsClient().GetPrintersContext(ctx, []string{"printer-name", "ppd-name", "printer-state", "printer-is-accepting-jobs"})
	if err != nil {
		if isNoPrintersError(err) {
			return []PrinterInfo{}, nil
		}
		return nil, err
	}
	result := make([]PrinterInfo, 0, len(printers))
	for name, attrs := range printers {
		ppd := attrString(attrs, "ppd-name")
		result = append(result, PrinterInfo{Name: name, PPDModel: ppd})
	}
	return result, nil
}

func (c *Client) PrintRaw(ctx context.Context, printer, filePath string) (int, error) {
	stat, err := os.Stat(filePath)
	if err != nil {
		return -1, err
	}
	f, err := os.Open(filePath)
	if err != nil {
		return -1, err
	}
	defer f.Close()

	mimeType, err := detectMimeType(f)
	if err != nil {
		return -1, err
	}

	return c.ippClient().PrintDocumentsContext(ctx, []ipp.Document{{
		Document: f,
		Name:     filepath.Base(filePath),
		Size:     int(stat.Size()),
		MimeType: mimeType,
	}}, printer, nil)
}

func detectMimeType(r io.ReadSeeker) (string, error) {
	header := make([]byte, 8)
	n, err := r.Read(header)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return "", err
	}

	sample := string(header[:n])
	switch {
	case strings.HasPrefix(sample, "%PDF-"):
		return "application/pdf", nil
	case strings.HasPrefix(sample, "%!PS-Adobe-"), strings.HasPrefix(sample, "%!PS"):
		return "application/postscript", nil
	default:
		return "application/octet-stream", nil
	}
}

func (c *Client) GetWampPrintQueues(ctx context.Context) (map[string]string, error) {
	printers, err := c.cupsClient().GetPrintersContext(ctx, []string{"printer-name", "device-uri"})
	if err != nil {
		if isNoPrintersError(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	result := make(map[string]string)
	for name, attrs := range printers {
		uri := attrString(attrs, "device-uri")
		if strings.HasPrefix(uri, "wampprint") {
			remote := remotePrinterFromDeviceURI(uri)
			result[remote] = name
		}
	}
	return result, nil
}

func (c *Client) CreateQueue(ctx context.Context, name, deviceURI, ppdModel string) error {
	if ppdModel == "" {
		ppdModel = "raw"
	}
	cl := c.cupsClient()
	if err := cl.CreatePrinterContext(ctx, name, deviceURI, ppdModel, false, "abort-job", "", ""); err != nil {
		return err
	}
	if err := cl.ResumePrinterContext(ctx, name); err != nil {
		return fmt.Errorf("resume printer %s: %w", name, err)
	}
	if err := cl.AcceptJobsContext(ctx, name); err != nil {
		return fmt.Errorf("accept jobs %s: %w", name, err)
	}
	return nil
}

func (c *Client) DeleteQueue(ctx context.Context, name string) error {
	return c.cupsClient().DeletePrinterContext(ctx, name)
}

func isNoPrintersError(err error) bool {
	var ippErr ipp.IPPError
	return errors.As(err, &ippErr) && ippErr.Status == ippStatusNoDestinations
}

func attrString(attrs ipp.Attributes, key string) string {
	if vals, ok := attrs[key]; ok && len(vals) > 0 {
		if s, ok := vals[0].Value.(string); ok {
			return s
		}
	}
	return ""
}

func remotePrinterFromDeviceURI(uri string) string {
	trimmed := strings.TrimSpace(uri)
	trimmed = strings.TrimPrefix(trimmed, "wampprint://")
	trimmed = strings.TrimPrefix(trimmed, "wampprint:/")
	return strings.TrimLeft(trimmed, "/")
}
