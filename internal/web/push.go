package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/basecamp/once-campfire-go/internal/database"
	"github.com/basecamp/once-campfire-go/internal/integrations"
	"github.com/basecamp/once-campfire-go/internal/jobs"
	"github.com/basecamp/once-campfire-go/internal/rails"
)

const pushPath = "/users/me/push_subscriptions"

func (s *Server) initJobs() {
	concurrency, _ := strconv.Atoi(os.Getenv("JOB_CONCURRENCY"))
	if concurrency < 1 {
		concurrency = 2
	}
	s.Jobs = jobs.New(concurrency, "push_message", "push", "webhook", "purge", "ban", "analyze")
	s.initCleanup()
	var vapid *integrations.VAPID
	if public, private := os.Getenv("VAPID_PUBLIC_KEY"), os.Getenv("VAPID_PRIVATE_KEY"); public != "" && private != "" {
		subject := os.Getenv("VAPID_SUBJECT")
		if subject == "" {
			domain := strings.TrimSpace(strings.Split(os.Getenv("TLS_DOMAIN"), ",")[0])
			if domain != "" {
				subject = "https://" + domain
			} else {
				subject = "https://github.com/basecamp/once-campfire-go"
			}
		}
		var err error
		vapid, err = integrations.NewVAPID(subject, public, private)
		if err != nil {
			slog.Error("Web Push disabled", "error", err)
		}
	}
	s.Push = integrations.NewPushSender(vapid)
	s.mux.HandleFunc("GET /users/{user_id}/push_subscriptions", s.auth(s.usersPushSubscriptionsIndex))
	s.mux.HandleFunc("POST /users/{user}/push_subscriptions", s.auth(s.pushSubscriptions))
	s.mux.HandleFunc("DELETE /users/{user}/push_subscriptions/{subscription}", s.auth(s.deletePushSubscription))
	s.mux.HandleFunc("POST /users/{user}/push_subscriptions"+"/{subscription}/test_notifications", s.auth(s.testPushNotification))
}
func subscriptionParams(r *http.Request) (map[string]*string, error) {
	attrs := map[string]*string{}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var raw map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			return nil, err
		}
		if nested, ok := raw["push_subscription"]; ok {
			if err := json.Unmarshal(nested, &raw); err != nil {
				return nil, err
			}
		}
		if len(raw) == 0 {
			return nil, errors.New("push_subscription is required")
		}
		for _, key := range []string{"endpoint", "p256dh_key", "auth_key"} {
			if value, ok := raw[key]; ok {
				var str *string
				if err := json.Unmarshal(value, &str); err == nil {
					attrs[key] = str
				}
			}
		}
		return attrs, nil
	}
	present := false
	for key := range r.Form {
		if strings.HasPrefix(key, "push_subscription[") {
			present = true
		}
	}
	if !present {
		return nil, errors.New("push_subscription is required")
	}
	for _, key := range []string{"endpoint", "p256dh_key", "auth_key"} {
		if r.Form.Has("push_subscription[" + key + "]") {
			str := r.Form.Get("push_subscription[" + key + "]")
			attrs[key] = &str
		}
	}
	return attrs, nil
}
func (s *Server) pushSubscriptions(w http.ResponseWriter, r *http.Request, u database.User) {
	attrs, err := subscriptionParams(r)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	existing, err := s.DB.FindPushSubscription(r.Context(), u.ID, attrs)
	if err != nil && !errors.Is(err, database.ErrNoRows) {
		s.fail(w, err)
		return
	}
	endpoint := existing.Endpoint
	if errors.Is(err, database.ErrNoRows) {
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
	id, _ := strconv.ParseInt(r.PathValue("subscription"), 10, 64)
	if err := s.DB.DeletePushSubscription(r.Context(), u.ID, id); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, s.origin(r)+pushPath, 302)
}
func notificationJSON(title, body, path string, badge int64) []byte {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	e.Encode(struct {
		Title   string `json:"title"`
		Options struct {
			Body string `json:"body"`
			Icon string `json:"icon"`
			Data struct {
				Path  string `json:"path"`
				Badge int64  `json:"badge"`
			} `json:"data"`
		} `json:"options"`
	}{title, struct {
		Body string `json:"body"`
		Icon string `json:"icon"`
		Data struct {
			Path  string `json:"path"`
			Badge int64  `json:"badge"`
		} `json:"data"`
	}{body, "/account/logo", struct {
		Path  string `json:"path"`
		Badge int64  `json:"badge"`
	}{path, badge}}})
	encoded, _ := rails.CanonicalJSON(bytes.TrimSpace(b.Bytes()), false)
	return encoded
}
func (s *Server) testPushNotification(w http.ResponseWriter, r *http.Request, u database.User) {
	id, _ := strconv.ParseInt(r.PathValue("subscription"), 10, 64)
	subscription, err := s.DB.PushSubscription(r.Context(), u.ID, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	badge, err := s.DB.UnreadCount(r.Context(), u.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	err = s.Push.Send(r.Context(), subscription.Endpoint, subscription.Key, subscription.Auth, notificationJSON("Campfire Test", database.UUID(), s.origin(r)+pushPath, badge))
	if err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, s.origin(r)+pushPath, 302)
}

func (s *Server) pushMessage(ctx context.Context, id int64, room database.Room) error {
	message, err := s.DB.Message(ctx, id)
	if errors.Is(err, database.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	mentions := s.mentionedIDs(ctx, message.Body)
	subscriptions, err := s.DB.PushRecipients(ctx, room.ID, message.CreatorID, mentions)
	if err != nil {
		return err
	}
	if len(subscriptions) == 0 {
		return nil
	}
	body := s.plainText(ctx, message.Body)
	if attachment, err := s.Storage.Attached(ctx, "Message", message.ID, "attachment"); err == nil && attachment.ID != 0 && strings.TrimSpace(body) == "" {
		body = attachment.Filename
	}
	title := room.Name
	if room.Type == "Rooms::Direct" {
		title = message.Creator
	} else {
		body = message.Creator + ": " + body
	}
	title = integrations.TruncatePush(title, 256)
	body = integrations.TruncatePush(body, 3072)
	for _, subscription := range subscriptions {
		badge, err := s.DB.UnreadCount(ctx, subscription.UserID)
		if err != nil {
			slog.Error("push badge failed", "error", err)
			continue
		}
		payload := notificationJSON(title, body, fmt.Sprintf("/rooms/%d", room.ID), badge)
		s.Jobs.Enqueue("push", func(ctx context.Context) error {
			err := s.Push.Send(ctx, subscription.Endpoint, subscription.Key, subscription.Auth, payload)
			if errors.Is(err, integrations.ErrPushGone) || errors.Is(err, integrations.ErrPushPoint) {
				return s.DB.DeletePushSubscription(ctx, subscription.UserID, subscription.ID)
			}
			return err
		})
	}
	return nil
}
