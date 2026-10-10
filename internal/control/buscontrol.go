// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package control

import (
	"context"
	"fmt"

	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/eventbus"
)

// BusControl implements AgentControl by publishing events on the cluster
// event bus. The OpAMP server (internal/opamp) on every replica subscribes to
// eventbus.AgentDesiredChanged and eventbus.AgentCommand and acts for the
// agents whose connection it holds. It lets the fleet management code be
// constructed (and tested) without the OpAMP server.
type BusControl struct {
	Bus eventbus.Bus
}

var _ AgentControl = (*BusControl)(nil)

// NewBusControl returns an AgentControl backed by bus.
func NewBusControl(bus eventbus.Bus) *BusControl { return &BusControl{Bus: bus} }

// DesiredStateChanged implements AgentControl (chunked events).
func (c *BusControl) DesiredStateChanged(ctx context.Context, ids []domain.AgentID) error {
	for _, chunk := range eventbus.ChunkAgentIDs(ids) {
		if err := c.Bus.Publish(ctx, eventbus.AgentDesiredChanged, eventbus.AgentIDsPayload{AgentIDs: chunk}); err != nil {
			return fmt.Errorf("publish desired change: %w", err)
		}
	}
	return nil
}

// Restart implements AgentControl (see the interface doc for preconditions).
func (c *BusControl) Restart(ctx context.Context, id domain.AgentID, requestedBy string) error {
	return c.command(ctx, id, eventbus.CommandRestart, requestedBy)
}

// RequestFullState implements AgentControl.
func (c *BusControl) RequestFullState(ctx context.Context, id domain.AgentID, requestedBy string) error {
	return c.command(ctx, id, eventbus.CommandReportFullState, requestedBy)
}

// Resync implements AgentControl.
func (c *BusControl) Resync(ctx context.Context, id domain.AgentID, requestedBy string) error {
	return c.command(ctx, id, eventbus.CommandResync, requestedBy)
}

func (c *BusControl) command(ctx context.Context, id domain.AgentID, cmd eventbus.CommandType, by string) error {
	return c.Bus.Publish(ctx, eventbus.AgentCommand, eventbus.AgentCommandPayload{AgentID: id, Command: cmd, RequestedBy: by})
}
