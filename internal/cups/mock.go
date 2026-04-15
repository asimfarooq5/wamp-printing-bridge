package cups

import (
	"context"
	"fmt"
)

type MockClient struct {
	PrinterInfos    []PrinterInfo
	WampprintQueues map[string]string
	PrintJobID      int

	GetErr       error
	CreateErr    error
	DeleteErr    error
	PrintInfoErr error

	PrintErr       error
	PrintFailFirst int

	PrintCallCount int
	Created        []string
	Deleted        []string
	Printed        []string
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

func (m *MockClient) GetWampPrintQueues(_ context.Context) (map[string]string, error) {
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
