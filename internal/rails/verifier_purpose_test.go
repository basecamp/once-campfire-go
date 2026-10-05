package rails

import "testing"

// The reference's signed_id::tests::combines_purposes.
func TestModelPurposeCombinesPurposes(t *testing.T) {
	for _, test := range []struct{ model, purpose, want string }{
		{"User", "avatar", "user/avatar"},
		{"User", "", "user"},
		{"Rooms::Open", "", "rooms/open"},
		{"HTTPRequest", "x", "http_request/x"},
		{"WebPush", "", "web_push"},
	} {
		if got := modelPurpose(test.model, test.purpose); got != test.want {
			t.Errorf("modelPurpose(%q, %q) = %q, want %q", test.model, test.purpose, got, test.want)
		}
	}
}
