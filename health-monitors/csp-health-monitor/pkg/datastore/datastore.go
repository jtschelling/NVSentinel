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

package datastore

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/nvidia/nvsentinel/data-models/pkg/model"
	"github.com/nvidia/nvsentinel/store-client/pkg/config"
	"github.com/nvidia/nvsentinel/store-client/pkg/datastore"
	_ "github.com/nvidia/nvsentinel/store-client/pkg/datastore/providers"
)

// Store defines the interface for datastore operations related to maintenance events.
type Store interface {
	UpsertMaintenanceEvent(ctx context.Context, event *model.MaintenanceEvent) error
	FindEventsToTriggerQuarantine(ctx context.Context, triggerTimeLimit time.Duration) ([]model.MaintenanceEvent, error)
	FindEventsToTriggerHealthy(ctx context.Context, healthyDelay time.Duration) ([]model.MaintenanceEvent, error)
	UpdateEventStatus(ctx context.Context, eventID string, newStatus model.InternalStatus) error
	GetLastProcessedEventTimestampByCSP(
		ctx context.Context,
		clusterName string,
		cspType model.CSP,
		cspNameForLog string,
	) (timestamp time.Time, found bool, err error)
	FindLatestActiveEventByNodeAndType(
		ctx context.Context,
		nodeName string,
		maintenanceType model.MaintenanceType,
		statuses []model.InternalStatus,
	) (*model.MaintenanceEvent, bool, error)
	FindLatestOngoingEventByNode(ctx context.Context, nodeName string) (*model.MaintenanceEvent, bool, error)
	FindActiveEventsByStatuses(ctx context.Context, csp model.CSP, statuses []string) ([]model.MaintenanceEvent, error)
}

// DatabaseStore implements the Store interface using the new store-client SDK.
type DatabaseStore struct {
	maintenanceStore datastore.MaintenanceEventStore
	dataStore        datastore.DataStore
}

var _ Store = (*DatabaseStore)(nil)

// NewStore creates a new database store client using the new store-client SDK.
func NewStore(ctx context.Context, databaseClientCertMountPath *string) (*DatabaseStore, error) {
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

	slog.Info("Database client initialized successfully using store-client",
		"provider", datastoreConfig.Provider)

	// Note: Index creation is not directly supported by the new store-client interface.
	// Indexes should be created manually or through database administration tools.
	// The following indexes are recommended for optimal performance:
	// 1. unique index on "eventId" field
	// 2. compound index on "status" + "scheduledStartTime" fields
	// 3. compound index on "status" + "actualEndTime" fields
	// 4. compound index on "csp" + "clusterName" + "eventReceivedTimestamp" (desc) fields
	// 5. index on "cspStatus" field

	return &DatabaseStore{
		maintenanceStore: ds.MaintenanceEventStore(),
		dataStore:        ds,
	}, nil
}

// UpsertMaintenanceEvent delegates to the MaintenanceEventStore implementation
func (s *DatabaseStore) UpsertMaintenanceEvent(ctx context.Context, event *model.MaintenanceEvent) error {
	return s.maintenanceStore.UpsertMaintenanceEvent(ctx, event)
}

// FindEventsToTriggerQuarantine delegates to the MaintenanceEventStore implementation
func (s *DatabaseStore) FindEventsToTriggerQuarantine(
	ctx context.Context,
	triggerTimeLimit time.Duration,
) ([]model.MaintenanceEvent, error) {
	return s.maintenanceStore.FindEventsToTriggerQuarantine(ctx, triggerTimeLimit)
}

// FindEventsToTriggerHealthy delegates to the MaintenanceEventStore implementation
func (s *DatabaseStore) FindEventsToTriggerHealthy(
	ctx context.Context,
	healthyDelay time.Duration,
) ([]model.MaintenanceEvent, error) {
	return s.maintenanceStore.FindEventsToTriggerHealthy(ctx, healthyDelay)
}

// UpdateEventStatus delegates to the MaintenanceEventStore implementation
func (s *DatabaseStore) UpdateEventStatus(ctx context.Context, eventID string, newStatus model.InternalStatus) error {
	return s.maintenanceStore.UpdateEventStatus(ctx, eventID, newStatus)
}

// GetLastProcessedEventTimestampByCSP delegates to the MaintenanceEventStore implementation
func (s *DatabaseStore) GetLastProcessedEventTimestampByCSP(
	ctx context.Context,
	clusterName string,
	cspType model.CSP,
	cspNameForLog string,
) (timestamp time.Time, found bool, err error) {
	return s.maintenanceStore.GetLastProcessedEventTimestampByCSP(ctx, clusterName, cspType, cspNameForLog)
}

// FindLatestActiveEventByNodeAndType delegates to the MaintenanceEventStore implementation
func (s *DatabaseStore) FindLatestActiveEventByNodeAndType(
	ctx context.Context,
	nodeName string,
	maintenanceType model.MaintenanceType,
	statuses []model.InternalStatus,
) (*model.MaintenanceEvent, bool, error) {
	return s.maintenanceStore.FindLatestActiveEventByNodeAndType(ctx, nodeName, maintenanceType, statuses)
}

// FindLatestOngoingEventByNode delegates to the MaintenanceEventStore implementation
func (s *DatabaseStore) FindLatestOngoingEventByNode(
	ctx context.Context,
	nodeName string,
) (*model.MaintenanceEvent, bool, error) {
	return s.maintenanceStore.FindLatestOngoingEventByNode(ctx, nodeName)
}

// FindActiveEventsByStatuses delegates to the MaintenanceEventStore implementation
func (s *DatabaseStore) FindActiveEventsByStatuses(
	ctx context.Context,
	csp model.CSP,
	statuses []string,
) ([]model.MaintenanceEvent, error) {
	return s.maintenanceStore.FindActiveEventsByStatuses(ctx, csp, statuses)
}
