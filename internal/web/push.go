package web

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"uuid"

	"github.com/basecamp/once-campfire-go/internal/database"
)

const pushPath = "/users/me/push_subscriptions"

func subscriptionParams(r *http.Request) (map[string]*string, error) {
	params, _ := r.Context().Value(structuredParamsKey{}).(map[string]any)
	root := params["push_subscription"]
	switch value := root.(type) {
	case nil:
		return nil, errors.New("push_subscription is required")
	case string:
		if strings.TrimSpace(value) == "" {
			return nil, errors.New("push_subscription is required")
		}
	case []any:
		if len(value) == 0 {
			return nil, errors.New("push_subscription is required")
		}
	case map[string]any:
		if len(value) == 0 {
			return nil, errors.New("push_subscription is required")
		}
	}
	attributes, _ := root.(map[string]any)
	attrs := map[string]*string{}
	for _, key := range []string{"endpoint", "p256dh_key", "auth_key"} {
		if value, present := attributes[key]; present {
			switch value := value.(type) {
			case nil:
				attrs[key] = nil
			case string:
				attrs[key] = &value
			case bool:
				text := strconv.FormatBool(value)
				attrs[key] = &text
			case json.Number:
				text := value.String()
				attrs[key] = &text
			}
		}
	}
	return attrs, nil
}

func (s *Server) pushSubscriptions(w http.ResponseWriter, r *http.Request, u database.User) {
	if r.Method == "GET" || r.Method == "HEAD" {
		list, err := s.DB.PushSubscriptions(r.Context(), u.ID)
		if err != nil {
			s.fail(w, err)
			return
		}
		s.respondPage(
			w,
			r,
			"push-subscriptions",
			200,
			page{User: u, Title: "Push notification subscriptions", Subscriptions: list},
		)
		return
	}
	attrs, err := subscriptionParams(r)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	existing, err := s.DB.FindPushSubscription(r.Context(), u.ID, attrs)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		s.fail(w, err)
		return
	}
	endpoint := existing.Endpoint
	if errors.Is(err, sql.ErrNoRows) {
		if value := attrs["endpoint"]; value != nil {
			endpoint = *value
		}
	}
	if err := s.Push.Validate(r.Context(), endpoint); err != nil {
		w.WriteHeader(422)
		return
	}
	if existing.ID != 0 {
		err = s.DB.TouchPushSubscription(r.Context(), existing.ID)
	} else {
		err = s.DB.SavePushSubscription(r.Context(), u.ID, attrs, r.UserAgent())
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(200)
}

func (s *Server) deletePushSubscription(w http.ResponseWriter, r *http.Request, u database.User) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err := s.DB.DeletePushSubscription(r.Context(), u.ID, id); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, s.origin(r)+pushPath, 302)
}

func (s *Server) testPushNotification(w http.ResponseWriter, r *http.Request, u database.User) {
	id, _ := strconv.ParseInt(r.PathValue("push_subscription_id"), 10, 64)
	delivery, err := s.NotificationQueries.TestPush(r.Context(), u.ID, id, "Campfire Test", uuid.NewV4().String(), s.origin(r)+pushPath)
	if err != nil {
		s.fail(w, err)
		return
	}
	subscription := delivery.Subscription
	err = s.Push.Send(r.Context(), subscription.Endpoint, subscription.Key, subscription.Auth, delivery.Payload)
	if err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, s.origin(r)+pushPath, 302)
}
