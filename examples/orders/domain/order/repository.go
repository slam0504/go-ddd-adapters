package order

import "github.com/slam0504/go-ddd-core/domain"

// Repository narrows the generic domain.Repository to Order.
type Repository = domain.Repository[*Order, ID]
