package domain

// OrderWithPayment is an order together with its payment (nil if it has none),
// read in one go for a screen that lists many orders.
type OrderWithPayment struct {
	Order   Order
	Payment *Payment
}
