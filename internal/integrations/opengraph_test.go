package integrations

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

type oracleDNS struct {
	hosts map[string][][]string
	calls map[string]int
}

func (d *oracleDNS) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	responses := d.hosts[host]
	if len(responses) == 0 {
		return nil, fmt.Errorf("unknown host")
	}
	n := d.calls[host]
	d.calls[host]++
	list := responses[min(n, len(responses)-1)]
	ips := []netip.Addr{}
	for _, s := range list {
		ips = append(ips, netip.MustParseAddr(s))
	}
	return ips, nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestOpenGraphOracle(t *testing.T) {
	var corpus struct {
		Hosts  map[string][][]string
		Routes []struct {
			Method, Host, Path string
			Status             int
			Headers            [][2]string
			Body               string
			Body64             string `json:"body_b64"`
			Repeat             []any  `json:"body_repeat"`
			Pad                int    `json:"pad_to"`
		}
		Cases []struct{ Name, URL string }
	}
	raw, err := os.ReadFile("../../reference/crates/campfire/src/integrations/testdata/opengraph_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	var expected []struct {
		Name     string
		Response struct {
			Status      int
			Body, Error string
		}
	}
	raw, err = os.ReadFile("../../reference/crates/campfire/src/integrations/testdata/opengraph_expected.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &expected); err != nil {
		t.Fatal(err)
	}
	for i, c := range corpus.Cases {
		t.Run(c.Name, func(t *testing.T) {
			dns := &oracleDNS{corpus.Hosts, map[string]int{}}
			client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if _, err := ResolvePublic(r.Context(), dns, r.URL.Hostname()); err != nil {
					return nil, err
				}
				for _, route := range corpus.Routes {
					if route.Host == r.URL.Hostname() && route.Method == r.Method && route.Path == r.URL.RequestURI() {
						body := route.Body
						if route.Body64 != "" {
							raw, _ := base64.StdEncoding.DecodeString(route.Body64)
							body = string(raw)
						}
						if len(route.Repeat) == 2 {
							body = strings.Repeat(route.Repeat[0].(string), int(route.Repeat[1].(float64)))
						}
						if route.Pad > len(body) {
							body += strings.Repeat(" ", route.Pad-len(body))
						}
						header := http.Header{}
						for _, pair := range route.Headers {
							header.Add(pair[0], pair[1])
						}
						length := int64(-1)
						if value := header.Get("Content-Length"); value != "" {
							length, _ = strconv.ParseInt(value, 10, 64)
						}
						return &http.Response{StatusCode: route.Status, Header: header, Body: io.NopCloser(strings.NewReader(body)), ContentLength: length, Request: r}, nil
					}
				}
				return nil, fmt.Errorf("unknown route %s %s", r.Method, r.URL)
			})}
			unfurler := Unfurler{client, dns}
			body, err := unfurler.Unfurl(context.Background(), c.URL)
			status := 200
			if err != nil {
				status = 500
			} else if body == nil {
				status = 204
			}
			want := expected[i].Response
			if expected[i].Name != c.Name {
				t.Fatal("case order mismatch")
			}
			if status != want.Status {
				t.Fatalf("status %d want %d: %v", status, want.Status, err)
			}
			if status == 200 {
				var gotValue, wantValue any
				json.Unmarshal(body, &gotValue)
				json.Unmarshal([]byte(want.Body), &wantValue)
				if !reflect.DeepEqual(gotValue, wantValue) {
					t.Errorf("body got %s want %s", body, want.Body)
				}
			}
		})
	}
}
