package cups

import (
	"context"
	"errors"
	"strings"

	ipp "github.com/phin1x/go-ipp"
)

// ippStatusNoDestinations is the IPP status code CUPS returns when no printers
// are configured (client-error-not-found, 0x0406).
const ippStatusNoDestinations = int16(1030)

// Manager abstracts CUPS operations so callers can be tested without a real printer.
type Manager interface {
	// ListPrinters returns the names of all printers known to CUPS.
	ListPrinters(ctx context.Context) ([]string, error)

	// PrintRaw submits filePath to the named printer as raw bytes (no PPD processing).
	PrintRaw(ctx context.Context, printer, filePath string) (int, error)

	// GetWampprintQueues returns every local CUPS queue whose device URI starts
	// with "wampprint", keyed by the remote printer name.
	// e.g. {"Office": "Remote_Office"}
	GetWampprintQueues(ctx context.Context) (map[string]string, error)

	// CreateQueue adds a raw CUPS queue backed by the given wampprint:// URI.
	CreateQueue(ctx context.Context, name, deviceURI string) error

	// DeleteQueue removes a CUPS queue by name.
	DeleteQueue(ctx context.Context, name string) error
}

// Client implements Manager against a live CUPS daemon via IPP.
type Client struct {
	host string
	port int
}

func NewClient(host string, port int) *Client {
	return &Client{host: host, port: port}
}

func (c *Client) cupsClient() *ipp.CUPSClient {
	return ipp.NewCUPSClient(c.host, c.port, "", "", false)
}

func (c *Client) ippClient() *ipp.IPPClient {
	return ipp.NewIPPClient(c.host, c.port, "", "", false)
}

func (c *Client) ListPrinters(ctx context.Context) ([]string, error) {
	printers, err := c.cupsClient().GetPrintersContext(ctx, []string{"printer-name"})
	if err != nil {
		if isNoPrintersError(err) {
			return []string{}, nil
		}
		return nil, err
	}
	names := make([]string, 0, len(printers))
	for name := range printers {
		names = append(names, name)
	}
	return names, nil
}

func (c *Client) PrintRaw(ctx context.Context, printer, filePath string) (int, error) {
	return c.ippClient().PrintFileContext(ctx, filePath, printer, map[string]any{
		"document-format": "application/octet-stream",
	})
}

func (c *Client) GetWampprintQueues(ctx context.Context) (map[string]string, error) {
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
			remote := strings.TrimSpace(strings.TrimPrefix(uri, "wampprint://"))
			result[remote] = name
		}
	}
	return result, nil
}

func (c *Client) CreateQueue(ctx context.Context, name, deviceURI string) error {
	return c.cupsClient().CreatePrinterContext(ctx, name, deviceURI, "raw", false, "stop-printer", "", "")
}

func (c *Client) DeleteQueue(ctx context.Context, name string) error {
	return c.cupsClient().DeletePrinterContext(ctx, name)
}

// isNoPrintersError reports whether err is the IPP error CUPS returns when no
// printers are configured ("No destinations added.", status 1030).
func isNoPrintersError(err error) bool {
	var ippErr ipp.IPPError
	return errors.As(err, &ippErr) && ippErr.Status == ippStatusNoDestinations
}

// attrString extracts the first string value for key from an IPP Attributes map.
func attrString(attrs ipp.Attributes, key string) string {
	if vals, ok := attrs[key]; ok && len(vals) > 0 {
		if s, ok := vals[0].Value.(string); ok {
			return s
		}
	}
	return ""
}
