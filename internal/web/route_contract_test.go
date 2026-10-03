package web

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

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
