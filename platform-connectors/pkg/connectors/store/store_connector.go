// Copyright (c) 2025, NVIDIA CORPORATION.  All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package store

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/nvidia/nvsentinel/data-models/pkg/model"
	"github.com/nvidia/nvsentinel/data-models/pkg/protos"
	"github.com/nvidia/nvsentinel/platform-connectors/pkg/ringbuffer"
	"github.com/nvidia/nvsentinel/store-client/pkg/config"
	"github.com/nvidia/nvsentinel/store-client/pkg/datastore"
	_ "github.com/nvidia/nvsentinel/store-client/pkg/datastore/providers"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

type DatabaseStoreConnector struct {
	// dataStore is the database-agnostic datastore
	dataStore datastore.DataStore
	// resourceSinkClients are client for pushing data to the resource count sink
	ringBuffer *ringbuffer.RingBuffer
	nodeName   string
}

func new(
	dataStore datastore.DataStore,
	ringBuffer *ringbuffer.RingBuffer,
	nodeName string,
) *DatabaseStoreConnector {
	return &DatabaseStoreConnector{
		dataStore:  dataStore,
		ringBuffer: ringBuffer,
		nodeName:   nodeName,
	}
}

func InitializeDatabaseStoreConnector(ctx context.Context, ringbuffer *ringbuffer.RingBuffer,
	clientCertMountPath string) (*DatabaseStoreConnector, error) {
	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		return nil, fmt.Errorf("NODE_NAME is not set")
	}

	// Load configuration from environment variables
	datastoreConfig, err := config.LoadDatastoreConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load datastore config: %w", err)
	}

	slog.Info("Creating datastore connection",
		"provider", datastoreConfig.Provider,
		"host", datastoreConfig.Connection.Host,
		"database", datastoreConfig.Connection.Database)

	// Create datastore instance using the factory
	ds, err := datastore.NewDataStore(ctx, *datastoreConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create datastore: %w", err)
	}

	// Ping to verify connection
	if err := ds.Ping(ctx); err != nil {
		return nil, fmt.Errorf("failed to ping datastore: %w", err)
	}

	slog.Info("Successfully initialized database store connector")

	return new(ds, ringbuffer, nodeName), nil
}

func (r *DatabaseStoreConnector) FetchAndProcessHealthMetric(ctx context.Context) {
	// Build an in-memory cache of entity states from existing documents in the database
	for {
		select {
		case <-ctx.Done():
			slog.Info("Context canceled, exiting health metric processing loop")
			return
		default:
			healthEvents := r.ringBuffer.Dequeue()
			if healthEvents == nil || len(healthEvents.GetEvents()) == 0 {
				continue
			}

			err := r.insertHealthEvents(ctx, healthEvents)
			if err != nil {
				slog.Error("Error inserting health events", "error", err)
				r.ringBuffer.HealthMetricEleProcessingFailed(healthEvents)
			} else {
				r.ringBuffer.HealthMetricEleProcessingCompleted(healthEvents)
			}
		}
	}
}

// Disconnect closes the database client connection
// Safe to call multiple times - will not error if already disconnected
func (r *DatabaseStoreConnector) Disconnect(ctx context.Context) error {
	if r.dataStore == nil {
		return nil
	}

	err := r.dataStore.Close(ctx)
	if err != nil {
		// Log but don't return error if already disconnected
		// This can happen in tests where mtest framework also disconnects
		slog.Warn("Error disconnecting database client (may already be disconnected)", "error", err)

		return nil
	}

	slog.Info("Successfully disconnected database client")

	return nil
}

func (r *DatabaseStoreConnector) insertHealthEvents(
	ctx context.Context,
	healthEvents *protos.HealthEvents,
) error {
	// Prepare all documents for batch insertion
	healthEventWithStatusList := make([]interface{}, 0, len(healthEvents.GetEvents()))

	for _, healthEvent := range healthEvents.GetEvents() {
		// CRITICAL FIX: Clone the HealthEvent to avoid pointer reuse issues with gRPC buffers
		// Without this clone, the healthEvent pointer may point to reused gRPC buffer memory
		// that gets overwritten by subsequent requests, causing data corruption in MongoDB.
		// This manifests as events having wrong isfatal/ishealthy/message values.
		clonedHealthEvent := proto.Clone(healthEvent).(*protos.HealthEvent)

		healthEventWithStatusObj := model.HealthEventWithStatus{
			CreatedAt:   time.Now().UTC(),
			HealthEvent: clonedHealthEvent,
		}
		healthEventWithStatusList = append(healthEventWithStatusList, healthEventWithStatusObj)
	}

	// Insert all documents in a single batch operation
	// This ensures MongoDB generates INSERT operations (not UPDATE) for change streams
	// Note: InsertMany is already atomic - either all documents are inserted or none are
	err := r.dataStore.InsertMany(ctx, healthEventWithStatusList)
	if err != nil {
		return fmt.Errorf("insertMany failed: %w", err)
	}

	return nil
}

func GenerateRandomObjectID() string {
	return uuid.New().String()
}
