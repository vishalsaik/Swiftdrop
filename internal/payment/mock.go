package payment

import (
	"context"

	"swiftdrop/internal/domain"
)

// MockGateway is a Gateway implementation for testing. By default it returns
// PaymentAuthorized. Override Result or Err to simulate declines and errors.
type MockGateway struct {
	Result *domain.PaymentResult
	Err    error
}

func (m *MockGateway) Charge(_ context.Context, _ domain.PaymentRequest) (*domain.PaymentResult, error) {
	if m.Err != nil {
		return nil, m.Err
	}
	if m.Result != nil {
		return m.Result, nil
	}
	return &domain.PaymentResult{
		PaymentID: "mock-pay-id",
		Status:    domain.PaymentAuthorized,
	}, nil
}
