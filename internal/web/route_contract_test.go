package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
)

func TestRouteDispatchUsesDecodedFormat(t *testing.T) {
	s := &Server{}
	s.dispatch = s.bindRoutes()
	for _, path := range []string{"/up.json", "//up.j%73on//"} {
		r := s.normalizeRequest(httptest.NewRequest("GET", "http://chat.test"+path+"?probe=one", nil))
		w := httptest.NewRecorder()
		s.routeHTTP(w, r)
		var body struct{ Status string }
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 200 || body.Status != "up" {
			t.Fatalf("%s: %d %s, %v", path, w.Code, w.Body.String(), err)
		}
	}
}

func TestProtocolRouteRejectionsPrecedeAuthentication(t *testing.T) {
	app, server, _, owner := testApp(t)
	rooms, err := app.DB.Rooms(context.Background(), owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ method, path string }{
		{"GET", "/%63able"},
		{"POST", "/cable"},
		{"POST", fmt.Sprintf("/rooms/%d/messages/messages", rooms[0].ID)},
		{"POST", fmt.Sprintf("/rooms/%d/key%%2Fextra/messages", rooms[0].ID)},
	} {
		response, _ := perform(t, server, tc.method, tc.path, "", nil, nil)
		if response.StatusCode != 404 {
			t.Errorf("%s %s reached authentication: %s", tc.method, tc.path, response.Status)
		}
	}
}

func TestReferenceRoutes(t *testing.T) {
	raw, err := os.ReadFile("../../reference/vectors/campfire_routes.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		Routes []struct {
			Verb, Path, Endpoint string
			Defaults             map[string]string
		}
		Recognitions []struct {
			Verb, Path string
			Endpoint   *string
			Params     map[string]string
			Error      any
		}
	}
	if err = json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	if len(vector.Routes) != len(contracts) {
		t.Fatalf("route count %d/%d", len(contracts), len(vector.Routes))
	}
	for i, want := range vector.Routes {
		got := contracts[i]
		if got.Method != want.Verb || got.Pattern != want.Path || got.Endpoint != want.Endpoint {
			t.Errorf("route %d: %v != %v", i, got, want)
		}
	}
	for _, c := range vector.Recognitions {
		t.Run(c.Verb+" "+c.Path, func(t *testing.T) {
			route, params, err := recognize(c.Verb, c.Path)
			if c.Error != nil {
				if err == nil {
					t.Fatal("expected invalid path")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.Endpoint == nil {
				if route != nil && route.Action != "missing_controller" {
					t.Fatal("unexpected", route.Endpoint)
				}
				return
			}
			if route == nil || route.Endpoint != *c.Endpoint {
				t.Fatalf("got %v, want %s", route, *c.Endpoint)
			}
			delete(params, "controller")
			delete(params, "action")
			if !reflect.DeepEqual(params, c.Params) {
				t.Errorf("params %v != %v", params, c.Params)
			}
		})
	}
}
