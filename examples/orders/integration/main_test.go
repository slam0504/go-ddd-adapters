//go:build integration

// Package integration runs the Kafka adapter against a real broker started
// via testcontainers. Tests are gated behind the `integration` build tag so
// `go test ./...` stays fast for everyday work.
package integration

import (
	"context"
	"fmt"
	"log"
	"os"
	"testing"

	"github.com/testcontainers/testcontainers-go/modules/redpanda"
)

const redpandaImage = "docker.redpanda.com/redpandadata/redpanda:v24.2.7"

// sharedBrokers is populated by TestMain with the broker address of a
// single redpanda container shared across all tests in this package.
// Sharing cuts ~3s × N tests down to one startup.
var sharedBrokers []string

func TestMain(m *testing.M) {
	ctx := context.Background()

	container, err := redpanda.Run(ctx, redpandaImage, redpanda.WithAutoCreateTopics())
	if err != nil {
		log.Fatalf("integration: start redpanda: %v", err)
	}
	defer func() {
		if terr := container.Terminate(ctx); terr != nil {
			log.Printf("integration: terminate redpanda: %v", terr)
		}
	}()

	broker, err := container.KafkaSeedBroker(ctx)
	if err != nil {
		log.Fatalf("integration: kafka broker: %v", err)
	}
	sharedBrokers = []string{broker}

	fmt.Fprintf(os.Stderr, "integration: redpanda ready at %s\n", broker)
	os.Exit(m.Run())
}
