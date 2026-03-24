package payment

import (
	"context"
	"fmt"
)

// Order represents a payment order
type Order struct {
	ID            string
	Amount        float64
	Currency      string
	PaymentMethod string
	CustomerID    string
}

// Receipt represents a payment receipt
type Receipt struct {
	OrderID       string
	TransactionID string
	Status        string
}

const approvalThreshold = 1000.0

// ProcessPayment orchestrates the payment flow
// REQ-042: Payment processing with validation and approval
// REQ-017: Approval workflow for high-value transactions
func ProcessPayment(ctx context.Context, order *Order) (*Receipt, error) {
	if err := validate(order); err != nil {
		return nil, fmt.Errorf("validation failed: %w", err)
	}

	if order.Amount > approvalThreshold {
		if err := requireApproval(ctx, order); err != nil {
			return nil, fmt.Errorf("approval failed: %w", err)
		}
	}

	txnID, err := charge(order.PaymentMethod, order.Amount)
	if err != nil {
		return nil, fmt.Errorf("charge failed: %w", err)
	}

	return &Receipt{
		OrderID:       order.ID,
		TransactionID: txnID,
		Status:        "completed",
	}, nil
}

// validate checks order fields
// REQ-005: Input validation for all transactions
func validate(order *Order) error {
	if order.ID == "" {
		return fmt.Errorf("order ID required")
	}
	if order.Amount <= 0 {
		return fmt.Errorf("amount must be positive")
	}
	if order.PaymentMethod == "" {
		return fmt.Errorf("payment method required")
	}
	return nil
}

// requireApproval requests approval for high-value orders
func requireApproval(ctx context.Context, order *Order) error {
	// In production, this would call an approval service
	fmt.Printf("Approval required for order %s: $%.2f\n", order.ID, order.Amount)
	return nil
}

// charge processes the actual payment
// REQ-043: Charge payment method securely
func charge(paymentMethod string, amount float64) (string, error) {
	// In production, this would call a payment gateway
	return fmt.Sprintf("txn_%s_%.0f", paymentMethod, amount), nil
}
