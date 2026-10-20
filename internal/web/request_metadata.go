package web

import (
	"context"
	"net/http"
	"sync"

	"github.com/basecamp/once-campfire-go/internal/useragent"
)

type requestInfoKey struct{}

// Ingress fixes transport facts once. Derived contexts share those facts and
// the request-owned routing, database observation, and lazy browser parsing.
type requestInfo struct {
	host, origin    string
	target, ip      string
	https           bool
	ipError         error
	response        *responseRound
	databaseVersion uint64
	routing         *recognizedRoute
	agentOnce       sync.Once
	agent           *useragent.Agent
}

func requestMetadata(ctx context.Context) *requestInfo {
	info, _ := ctx.Value(requestInfoKey{}).(*requestInfo)
	return info
}

func requestAgent(r *http.Request) useragent.Agent {
	if info := requestMetadata(r.Context()); info != nil {
		info.agentOnce.Do(func() {
			agent := useragent.Parse(r.UserAgent())
			info.agent = &agent
		})
		return *info.agent
	}
	// Direct rendering and background helpers do not have an HTTP entry context.
	return useragent.Parse(r.UserAgent())
}
