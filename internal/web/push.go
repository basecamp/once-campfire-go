package web

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

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
	s.Jobs = jobs.New(concurrency, "push", "webhook", "purge", "ban", "analyze")
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
	s.Jobs.RegisterDurable("push", s.drainPushOutbox)
	s.mux.HandleFunc("GET /users/{user}/push_subscriptions", s.auth(s.pushSubscriptions))
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
	if r.Method == "GET" || r.Method == "HEAD" {
		list, err := s.DB.PushSubscriptions(r.Context(), u.ID)
		if err != nil {
			s.fail(w, err)
			return
		}
		s.render(w, r, "push-subscriptions", 200, page{User: u, Title: "Push notification subscriptions", Subscriptions: list})
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

// messageCreated broadcasts unread bumps and wakes the durable push scan.
// Delivery rehydrates the message and mentions from persisted state, so the
// same path works after a crash with no request-local message snapshot.
//
// The unread bump owns its budget (deliveryTimeout) and opens it when the hub
// reports work — a server with no unread subscriber opens no timer, while a
// server that has one opens a timer and reads the room's members even if that
// room turns out to be unsubscribed. Either way a slow room broadcast cannot
// leave this stage with a spent deadline (Lever 5).
func (s *Server) messageCreated(message database.Message, room database.Room, mentioned []int64) {
	if err := s.Cable.PublishUnreadBump(room.ID, deliveryTimeout); err != nil {
		slog.Error("unread notification failed", "error", err)
	}
	// Recipients, badge counts and delivery run off the posting request.
	// Saturation loses only this wake; periodic scanning recovers the intent.
	s.Jobs.WakeDurable("push")
}

// drainPushOutbox runs serialized by the durable job registration. Delivery
// is at-least-once: a crash after the push service accepts a message but before
// acknowledgement may repeat it. Disabled push intentionally discards intent.
// Websocket broadcasts remain best effort and are never replayed.
func (s *Server) drainPushOutbox(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, pushRequestTimeout)
	defer cancel()
	var storage [128]int64
	for {
		ids, err := s.DB.DuePush(ctx, storage[:0], len(storage))
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		var first error
		var progressStorage [128]database.PushProgress
		progress := progressStorage[:0]
		var outcomeStorage [128]database.PushOutcome
		outcomes := outcomeStorage[:0]
		for _, id := range ids {
			if ctx.Err() != nil || len(progress) == cap(progress) {
				break
			}
			outcome := database.PushOutcome{MessageID: id, Complete: true}
			if s.Push.VAPID != nil {
				message, err := s.DB.Message(ctx, id)
				if err == nil {
					var room database.Room
					room, err = s.DB.PushRoom(ctx, message.RoomID)
					if err == nil {
						progress, outcome, err = s.deliverPushProgress(ctx, message, room, progress)
					}
				}
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					if outcome.Complete {
						outcome.Complete = false
						outcome.RetryAt = time.Now().Add(30 * time.Second).Unix()
					}
					if first == nil {
						first = err
					}
				}
			}
			outcomes = append(outcomes, outcome)
		}
		// Network delivery can exhaust the scan context. Persist its completed
		// prefix once, using a separate short budget, before returning.
		saveCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		err = s.DB.SavePushProgress(saveCtx, progress, outcomes)
		stop()
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if first != nil {
			return first
		}
	}
}

// pushRequestTimeout bounds one push job's queries and delivery attempts, the
// same budget the inline path gave the whole chain.
const pushRequestTimeout = 5 * time.Second

// deliveryTimeout is the budget of one post-commit delivery stage: the room
// broadcast, the unread bump, the webhook candidate lookup. Each stage opens it
// when it starts its own work (Lever 5), so a stage that is slow or fails
// cannot consume another stage's budget — the three stages used to share one
// context, and a slow broadcast then starved the notification and webhook
// stages that still had work. The push and webhook deliveries themselves run
// later on the job workers under their own bounds.
//
// It is not a promise that no timer is ever created: the broadcast's is opened
// by its caller for every post, and the unread bump's is opened whenever any
// client on the server holds an unread subscription, before it reads the room's
// members. What is free is a stage with no work to start: an empty mention list
// and a server with no unread subscriber.
//
// A var, like the cable package's frame-cache bounds, so tests can shorten it.
var deliveryTimeout = 5 * time.Second

// deliverPushProgress is the moved Room::PushMessageJob: the recipient query, the
// per-subscriber badge and payload, and the delivery attempts, all after the
// posting request has responded (the reference runs this job async).
func (s *Server) deliverPushProgress(ctx context.Context, message database.Message, room database.Room, progress []database.PushProgress) ([]database.PushProgress, database.PushOutcome, error) {
	// Partial work remains due, but yields to older scheduled messages rather
	// than retaining retry_at=0 forever during a fan-out longer than one budget.
	outcome := database.PushOutcome{MessageID: message.ID, RetryAt: time.Now().Unix()}
	mentioned := s.mentionedIDs(ctx, message.Body)
	mentionedSet := make(map[int64]struct{}, len(mentioned))
	for _, id := range mentioned {
		mentionedSet[id] = struct{}{}
	}
	remaining := cap(progress) - len(progress)
	subscriptions, err := s.DB.PendingPushRecipients(ctx, message.ID, room.ID, message.CreatorID, mentioned, remaining+1)
	if err != nil {
		outcome.RetryAt = time.Now().Add(30 * time.Second).Unix()
		return progress, outcome, err
	}
	if len(subscriptions) == 0 {
		outcome.Complete = true
		return progress, outcome, nil
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
	var first error
	for i, subscription := range subscriptions {
		if subscription.RetryAt > time.Now().Unix() {
			// Candidates are ordered by persisted attempt time. Once a sleeping
			// failure is reached, every remaining candidate is also sleeping,
			// including candidates beyond this bounded page. Do not acknowledge
			// them or restart the round; let later messages use the scan budget.
			outcome.RetryAt = subscription.RetryAt
			return progress, outcome, first
		}
		if ctx.Err() != nil || i == remaining {
			// Untouched suffix stays immediately due, ahead of failed recipients.
			return progress, outcome, first
		}
		_, isMentioned := mentionedSet[subscription.UserID]
		current, err := s.DB.CurrentPushRecipient(ctx, subscription.ID, subscription.UserID, room.ID, message.CreatorID, isMentioned)
		if errors.Is(err, sql.ErrNoRows) {
			progress = append(progress, database.PushProgress{MessageID: message.ID, SubscriptionID: subscription.ID, Completed: true})
			continue
		}
		if err != nil {
			return progress, outcome, err
		}
		subscription = current
		badge, err := s.DB.UnreadCount(ctx, subscription.UserID)
		if err != nil {
			return progress, outcome, err
		}
		payload := notificationJSON(title, body, fmt.Sprintf("/rooms/%d", room.ID), badge)
		err = s.Push.Send(ctx, subscription.Endpoint, subscription.Key, subscription.Auth, payload)
		gone := false
		if err != nil {
			if errors.Is(err, integrations.ErrPushGone) || errors.Is(err, integrations.ErrPushPoint) {
				gone = true
				err = nil
			}
			if err != nil && first == nil {
				first = err
			}
		}
		progress = append(progress, database.PushProgress{MessageID: message.ID, SubscriptionID: subscription.ID, Completed: err == nil, Gone: gone})
	}
	outcome.Complete = first == nil
	if first != nil {
		outcome.RetryAt = time.Now().Add(31 * time.Second).Unix()
	}
	return progress, outcome, first
}
