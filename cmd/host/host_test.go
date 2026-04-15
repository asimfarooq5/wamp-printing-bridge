package main

import (
	"context"
	"errors"
	"testing"

	"github.com/xconnio/wamp_printer_bridge/internal/cups"
)

func init() {
	printRetryDelay = 0
}

func TestExecutePrint_Success(t *testing.T) {
	mock := &cups.MockClient{PrintJobID: 42}
	cupsManager = mock
	session = nil

	executePrint(context.Background(), "job-1", "HP_LaserJet", "doc.pdf", []byte("PDF content"))

	if mock.PrintCallCount != 1 {
		t.Errorf("expected 1 print attempt, got %d", mock.PrintCallCount)
	}
	if len(mock.Printed) != 1 {
		t.Errorf("expected 1 successful print, got %d", len(mock.Printed))
	}
}

func TestExecutePrint_AllAttemptsFail(t *testing.T) {
	mock := &cups.MockClient{
		PrintErr: errors.New("printer offline"),
	}
	cupsManager = mock
	session = nil

	executePrint(context.Background(), "job-2", "HP_LaserJet", "doc.pdf", []byte("PDF content"))

	if mock.PrintCallCount != printMaxRetries {
		t.Errorf("expected %d attempts, got %d", printMaxRetries, mock.PrintCallCount)
	}
	if len(mock.Printed) != 0 {
		t.Errorf("expected no successful prints, got %d", len(mock.Printed))
	}
}

func TestExecutePrint_RetryThenSucceed(t *testing.T) {
	mock := &cups.MockClient{
		PrintJobID:     7,
		PrintErr:       errors.New("printer busy"),
		PrintFailFirst: 2,
	}
	cupsManager = mock
	session = nil

	executePrint(context.Background(), "job-3", "HP_LaserJet", "doc.pdf", []byte("PDF content"))

	if mock.PrintCallCount != 3 {
		t.Errorf("expected 3 attempts, got %d", mock.PrintCallCount)
	}
	if len(mock.Printed) != 1 {
		t.Errorf("expected 1 successful print after retries, got %d", len(mock.Printed))
	}
}

func TestExecutePrint_EmptyData(t *testing.T) {
	mock := &cups.MockClient{PrintJobID: 1}
	cupsManager = mock
	session = nil

	executePrint(context.Background(), "job-4", "HP_LaserJet", "empty.pdf", []byte{})

	if mock.PrintCallCount != 1 {
		t.Errorf("expected 1 print attempt, got %d", mock.PrintCallCount)
	}
}

func TestExecutePrint_ContextCancelled(t *testing.T) {
	mock := &cups.MockClient{
		PrintErr:       errors.New("printer busy"),
		PrintFailFirst: 0,
	}
	cupsManager = mock
	session = nil

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	executePrint(ctx, "job-5", "HP_LaserJet", "doc.pdf", []byte("PDF"))

	if mock.PrintCallCount > 1 {
		t.Errorf("expected ≤1 attempt with cancelled context, got %d", mock.PrintCallCount)
	}
}

func TestListPrinters_ReturnsMockPrinters(t *testing.T) {
	cupsManager = &cups.MockClient{
		PrinterInfos: []cups.PrinterInfo{
			{Name: "HP_LaserJet", PPDModel: "drv:///sample.drv/generic.ppd"},
			{Name: "Canon_PIXMA", PPDModel: "drv:///sample.drv/generic.ppd"},
		},
	}

	printers, err := cupsManager.GetPrintersInfo(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(printers) != 2 {
		t.Fatalf("expected 2 printers, got %d: %v", len(printers), printers)
	}
}

func TestListPrinters_Empty(t *testing.T) {
	cupsManager = &cups.MockClient{PrinterInfos: []cups.PrinterInfo{}}

	printers, err := cupsManager.GetPrintersInfo(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(printers) != 0 {
		t.Errorf("expected empty list, got %v", printers)
	}
}

func TestListPrinters_Error(t *testing.T) {
	cupsManager = &cups.MockClient{PrintInfoErr: errors.New("CUPS unavailable")}

	_, err := cupsManager.GetPrintersInfo(context.Background())
	if err == nil {
		t.Error("expected error, got nil")
	}
}
