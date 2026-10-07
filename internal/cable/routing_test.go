package cable

import "testing"

// Protocol delivery/auth tests cover the observable contract. This case protects
// inverse positions and reference release during swap-removal and route changes.
func TestRoutingBucketsMaintainPositionsAndReleaseSockets(t *testing.T) {
	h := New(nil, nil)
	clients := []*client{
		{subscriptions: make(map[string]subscription)},
		{subscriptions: make(map[string]subscription)},
		{subscriptions: make(map[string]subscription)},
	}
	remove := func(c *client, id string) {
		h.unindex(c.subscriptions[id])
		delete(c.subscriptions, id)
	}
	check := func() {
		t.Helper()
		for key, bucket := range h.subscribers {
			for i, r := range bucket {
				sub, found := r.client.subscriptions[r.identifier]
				if !found || sub.position != i || sub.Room != r.room || destination(sub) != key {
					t.Fatal("routing position/scope diverged from subscription owner")
				}
			}
			for _, unused := range bucket[len(bucket):cap(bucket)] {
				if unused.client != nil || unused.identifier != "" {
					t.Fatal("spare capacity retains disconnected subscriber")
				}
			}
		}
		for _, c := range clients {
			for id, sub := range c.subscriptions {
				key := destination(sub)
				if key != (publication{}) && h.subscribers[key][sub.position] != (recipient{c, id, sub.Room}) {
					t.Fatal("subscription missing from routing bucket")
				}
			}
		}
	}
	for _, c := range clients {
		h.setSubscription(c, "presence", subscription{Channel: "PresenceChannel", Room: 12, Stream: "presence:12", Present: true})
		h.setSubscription(c, "room", subscription{Channel: "RoomMessagesChannel", Room: 12})
		h.setSubscription(c, "alias", subscription{Channel: "RoomMessagesChannel", Room: 12})
	}
	check()
	remove(clients[0], "room") // Swap a different socket's alias into the first slot.
	check()
	for _, c := range clients {
		sub := c.subscriptions["presence"]
		sub.Present = false
		h.setSubscription(c, "presence", sub)
		check()
	}
	h.setSubscription(clients[1], "alias", subscription{Channel: "RoomMessagesChannel", Room: 13})
	check()
	// Even a shared stream changing authorization scope must replace its entry.
	h.setSubscription(clients[1], "presence", subscription{Channel: "PresenceChannel", Room: 13, Stream: "presence:12"})
	check()
	for _, c := range clients {
		for id := range c.subscriptions {
			remove(c, id)
			check()
		}
	}
	if len(h.subscribers) != 0 {
		t.Fatal("empty routing buckets retained")
	}
}
