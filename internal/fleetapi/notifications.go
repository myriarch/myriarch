// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package fleetapi

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/myriarch/myriarch/internal/api"
	"github.com/myriarch/myriarch/internal/api/dto"
	"github.com/myriarch/myriarch/internal/domain"
	"github.com/myriarch/myriarch/internal/notify"
)

func (s *service) notificationRoutes() []api.Route {
	return []api.Route{
		route(http.MethodGet, "/notifications/channels", "listNotificationChannels", "read", s.listChannels),
		route(http.MethodPost, "/notifications/channels/{name}/actions/test", "testNotificationChannel", "notifications:test", s.testChannel),
	}
}

func (s *service) listChannels(w http.ResponseWriter, r *http.Request) {
	api.WriteJSON(w, http.StatusOK, api.NewList[notify.ChannelInfo](s.d.Notifier.Channels()))
}

func (s *service) testChannel(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	err := s.d.Notifier.Test(r.Context(), name)
	s.recordAudit(r, "notification.test", "notification_channel", name, nil, err)
	if err != nil {
		if !errors.Is(err, domain.ErrNotFound) {
			// A failing destination is an operational problem, not a client error.
			err = fmt.Errorf("%w: %v", domain.ErrUnavailable, err)
		}
		api.WriteError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusAccepted, dto.ActionAccepted{Accepted: true, Message: "test notification sent"})
}

func isInvalidArgument(err error) bool { return errors.Is(err, domain.ErrInvalidArgument) }
