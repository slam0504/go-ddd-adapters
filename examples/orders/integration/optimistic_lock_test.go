//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/slam0504/go-ddd-core/application"
	"github.com/slam0504/go-ddd-core/domain"

	pgxoutbox "github.com/slam0504/go-ddd-adapters/eventbus/outbox/pgx"
	apporder "github.com/slam0504/go-ddd-adapters/examples/orders/application/order"
	orderdom "github.com/slam0504/go-ddd-adapters/examples/orders/domain/order"
	"github.com/slam0504/go-ddd-adapters/examples/orders/infra/eventcodec"
	"github.com/slam0504/go-ddd-adapters/examples/orders/infra/pgxrepo"
	pgxdb "github.com/slam0504/go-ddd-adapters/ports/database/pgx"
)

// TestSave_OptimisticLock_ConcurrentUpdate exercises the SQL guard
// directly at the pgxrepo layer: two snapshots of the same row both
// transition to shipped in memory; the first Save wins, the second
// must return ErrConcurrencyConflict. The DB must show exactly one
// shipped row at version=2 (no double-apply, no revert to seed state).
func TestSave_OptimisticLock_ConcurrentUpdate(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	repo := pgxrepo.New(sharedPool)

	const orderID orderdom.ID = "ord-ol-concurrent"
	seed := orderdom.Hydrate(
		orderID,
		"alice",
		orderdom.StatusPlaced,
		1, // version
		99,
		[]orderdom.Item{{SKU: "A", Quantity: 1, PriceCents: 99}},
	)
	if err := repo.Save(ctx, seed); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	findA, err := repo.FindByID(ctx, orderID)
	if err != nil {
		t.Fatalf("findA: %v", err)
	}
	findB, err := repo.FindByID(ctx, orderID)
	if err != nil {
		t.Fatalf("findB: %v", err)
	}

	if err := findA.Ship(uuid.NewString(), "carrierA"); err != nil {
		t.Fatalf("ship A: %v", err)
	}
	if err := findB.Ship(uuid.NewString(), "carrierB"); err != nil {
		t.Fatalf("ship B: %v", err)
	}

	if err := repo.Save(ctx, findA); err != nil {
		t.Fatalf("save A: want nil, got %v", err)
	}

	err = repo.Save(ctx, findB)
	if !errors.Is(err, domain.ErrConcurrencyConflict) {
		t.Fatalf("save B: want ErrConcurrencyConflict, got %v", err)
	}

	// DB shows exactly one shipped transition.
	// Note: Ship() does not mutate items/total_cents, so findA vs findB
	// cannot be distinguished on the orders row alone — the assertion
	// is that version advanced by exactly one and status moved to shipped.
	var (
		status  string
		version int64
	)
	if err := sharedPool.QueryRow(ctx,
		`SELECT status, version FROM orders WHERE id=$1`,
		string(orderID),
	).Scan(&status, &version); err != nil {
		t.Fatalf("post query: %v", err)
	}
	if status != string(orderdom.StatusShipped) {
		t.Errorf("status: want %q, got %q", orderdom.StatusShipped, status)
	}
	if version != 2 {
		t.Errorf("version: want 2, got %d", version)
	}
}

// TestPlaceOrder_DuplicateID_Conflict drives the same conflict through
// the PlaceOrderHandler: a second PlaceOrderCommand with the same
// OrderID must return ErrConcurrencyConflict, and the tx rollback
// must leave exactly one outbox_records row (the first call's stage)
// rather than two.
func TestPlaceOrder_DuplicateID_Conflict(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	codec := eventcodec.New()
	uow := application.UnitOfWorkFromTxManager(pgxdb.NewTxManager(sharedPool))
	repo := pgxrepo.New(sharedPool)
	store, err := pgxoutbox.NewStore(pgxoutbox.Config{Pool: sharedPool, Codec: codec})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	const topic = "orders.placed.duplicate.test"
	handler := apporder.NewPlaceOrderHandler(uow, repo, store, topic, uuid.NewString)

	cmd := apporder.PlaceOrderCommand{
		OrderID:    "ord-dup",
		CustomerID: "alice",
		Items:      []orderdom.Item{{SKU: "A", Quantity: 1, PriceCents: 99}},
	}

	var preCount int64
	if err := sharedPool.QueryRow(ctx,
		`SELECT COUNT(*) FROM outbox_records WHERE aggregate_id=$1`,
		cmd.OrderID,
	).Scan(&preCount); err != nil {
		t.Fatalf("pre count: %v", err)
	}
	if preCount != 0 {
		t.Fatalf("pre outbox count: want 0, got %d", preCount)
	}

	if _, err := handler.Handle(ctx, cmd); err != nil {
		t.Fatalf("first place: want nil, got %v", err)
	}

	_, err = handler.Handle(ctx, cmd)
	if !errors.Is(err, domain.ErrConcurrencyConflict) {
		t.Fatalf("second place: want ErrConcurrencyConflict, got %v", err)
	}

	var postCount int64
	if err := sharedPool.QueryRow(ctx,
		`SELECT COUNT(*) FROM outbox_records WHERE aggregate_id=$1`,
		cmd.OrderID,
	).Scan(&postCount); err != nil {
		t.Fatalf("post count: %v", err)
	}
	if postCount != 1 {
		t.Errorf("outbox rows after conflict: want 1 (first call's stage only), got %d", postCount)
	}
}
