package cups

import (
	"context"
	"fmt"
)

// MockClient is a Manager implementation for use in tests.
// Populate the fields before use; record fields are appended on each call.
type MockClient struct {
	// Canned responses
	PrinterInfos    []PrinterInfo
	WampprintQueues map[string]string
	PrintJobID      int

	// Error overrides
	GetErr       error
	CreateErr    error
	DeleteErr    error
	PrintInfoErr error

	// PrintErr is returned on print calls.
	// If PrintFailFirst > 0, it is returned only on the first PrintFailFirst calls;
	// subsequent calls succeed. If PrintFailFirst == 0, every call fails.
	PrintErr       error
	PrintFailFirst int

	// Call records
	PrintCallCount int
	Created        []string // name values passed to CreateQueue
	Deleted        []string // name values passed to DeleteQueue
	Printed        []string // "printer:filePath" pairs from successful PrintRaw calls
}

func (m *MockClient) GetPrintersInfo(_ context.Context) ([]PrinterInfo, error) {
	if m.PrintInfoErr != nil {
		return nil, m.PrintInfoErr
	}
	return m.PrinterInfos, nil
}

func (m *MockClient) PrintRaw(_ context.Context, printer, filePath string) (int, error) {
	m.PrintCallCount++
	shouldFail := m.PrintErr != nil && (m.PrintFailFirst == 0 || m.PrintCallCount <= m.PrintFailFirst)
	if shouldFail {
		return 0, m.PrintErr
	}
	m.Printed = append(m.Printed, fmt.Sprintf("%s:%s", printer, filePath))
	return m.PrintJobID, nil
}

func (m *MockClient) GetWampprintQueues(_ context.Context) (map[string]string, error) {
	if m.GetErr != nil {
		return nil, m.GetErr
	}
	if m.WampprintQueues == nil {
		return map[string]string{}, nil
	}
	return m.WampprintQueues, nil
}

func (m *MockClient) CreateQueue(_ context.Context, name, _, _ string) error {
	if m.CreateErr != nil {
		return m.CreateErr
	}
	m.Created = append(m.Created, name)
	return nil
}

func (m *MockClient) DeleteQueue(_ context.Context, name string) error {
	if m.DeleteErr != nil {
		return m.DeleteErr
	}
	m.Deleted = append(m.Deleted, name)
	return nil
}
