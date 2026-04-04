package main

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"

	"github.com/xconnio/wamp_printer_bridge/internal/cups"
)

// -------------------------------------------------------
// computePrinterDiff — pure logic, no CUPS or WAMP needed
// -------------------------------------------------------

func TestComputePrinterDiff_CreateAll(t *testing.T) {
	toCreate, toDelete := computePrinterDiff(
		[]string{"A", "B"},
		map[string]string{},
	)
	sort.Strings(toCreate)
	if !reflect.DeepEqual(toCreate, []string{"A", "B"}) {
		t.Errorf("toCreate: got %v, want [A B]", toCreate)
	}
	if len(toDelete) != 0 {
		t.Errorf("toDelete: got %v, want []", toDelete)
	}
}

func TestComputePrinterDiff_DeleteAll(t *testing.T) {
	toCreate, toDelete := computePrinterDiff(
		[]string{},
		map[string]string{"A": "Remote_A", "B": "Remote_B"},
	)
	sort.Strings(toDelete)
	if len(toCreate) != 0 {
		t.Errorf("toCreate: got %v, want []", toCreate)
	}
	if !reflect.DeepEqual(toDelete, []string{"Remote_A", "Remote_B"}) {
		t.Errorf("toDelete: got %v, want [Remote_A Remote_B]", toDelete)
	}
}

func TestComputePrinterDiff_NoDiff(t *testing.T) {
	toCreate, toDelete := computePrinterDiff(
		[]string{"A", "B"},
		map[string]string{"A": "Remote_A", "B": "Remote_B"},
	)
	if len(toCreate) != 0 || len(toDelete) != 0 {
		t.Errorf("expected no diff, got create=%v delete=%v", toCreate, toDelete)
	}
}

func TestComputePrinterDiff_CreateSomeDeleteSome(t *testing.T) {
	toCreate, toDelete := computePrinterDiff(
		[]string{"A", "C"},
		map[string]string{"A": "Remote_A", "B": "Remote_B"},
	)
	if !reflect.DeepEqual(toCreate, []string{"C"}) {
		t.Errorf("toCreate: got %v, want [C]", toCreate)
	}
	if !reflect.DeepEqual(toDelete, []string{"Remote_B"}) {
		t.Errorf("toDelete: got %v, want [Remote_B]", toDelete)
	}
}

func TestComputePrinterDiff_EmptyBoth(t *testing.T) {
	toCreate, toDelete := computePrinterDiff(nil, map[string]string{})
	if len(toCreate) != 0 || len(toDelete) != 0 {
		t.Errorf("expected both empty, got create=%v delete=%v", toCreate, toDelete)
	}
}

// -------------------------------------------------------
// Sync behaviour via MockClient — no printer, no WAMP needed
// -------------------------------------------------------

func TestSync_CreatesNewQueues(t *testing.T) {
	mock := &cups.MockClient{WampprintQueues: map[string]string{}}
	cupsManager = mock

	toCreate, toDelete := computePrinterDiff(
		[]string{"Office", "Home"},
		map[string]string{},
	)
	for _, p := range toCreate {
		if err := cupsManager.CreateQueue(context.Background(), "Remote_"+p, "wampprint://"+p); err != nil {
			t.Fatalf("CreateQueue: %v", err)
		}
	}
	if len(toDelete) != 0 {
		t.Errorf("unexpected deletes: %v", toDelete)
	}

	sort.Strings(mock.Created)
	if !reflect.DeepEqual(mock.Created, []string{"Remote_Home", "Remote_Office"}) {
		t.Errorf("Created: got %v, want [Remote_Home Remote_Office]", mock.Created)
	}
}

func TestSync_DeletesStaleQueues(t *testing.T) {
	mock := &cups.MockClient{
		WampprintQueues: map[string]string{"OldPrinter": "Remote_OldPrinter"},
	}
	cupsManager = mock

	local, _ := cupsManager.GetWampprintQueues(context.Background())
	_, toDelete := computePrinterDiff([]string{}, local)

	for _, q := range toDelete {
		if err := cupsManager.DeleteQueue(context.Background(), q); err != nil {
			t.Fatalf("DeleteQueue: %v", err)
		}
	}
	if !reflect.DeepEqual(mock.Deleted, []string{"Remote_OldPrinter"}) {
		t.Errorf("Deleted: got %v, want [Remote_OldPrinter]", mock.Deleted)
	}
}

func TestSync_CreateQueueError(t *testing.T) {
	mock := &cups.MockClient{CreateErr: errors.New("lpadmin failed")}
	cupsManager = mock

	err := cupsManager.CreateQueue(context.Background(), "Remote_X", "wampprint://X")
	if err == nil {
		t.Error("expected error, got nil")
	}
	if len(mock.Created) != 0 {
		t.Errorf("expected nothing created on error, got %v", mock.Created)
	}
}

func TestSync_GetQueuesError(t *testing.T) {
	mock := &cups.MockClient{GetErr: errors.New("CUPS unavailable")}
	cupsManager = mock

	_, err := cupsManager.GetWampprintQueues(context.Background())
	if err == nil {
		t.Error("expected error, got nil")
	}
}

func TestSync_NoChangesWhenInSync(t *testing.T) {
	mock := &cups.MockClient{
		WampprintQueues: map[string]string{
			"Office": "Remote_Office",
			"Home":   "Remote_Home",
		},
	}
	cupsManager = mock

	local, _ := cupsManager.GetWampprintQueues(context.Background())
	toCreate, toDelete := computePrinterDiff([]string{"Office", "Home"}, local)

	if len(toCreate) != 0 || len(toDelete) != 0 {
		t.Errorf("expected no changes, got create=%v delete=%v", toCreate, toDelete)
	}
	if len(mock.Created) != 0 || len(mock.Deleted) != 0 {
		t.Errorf("expected no CUPS calls, got created=%v deleted=%v", mock.Created, mock.Deleted)
	}
}
