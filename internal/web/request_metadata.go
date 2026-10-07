package web

import (
	"context"
	"net/http"
	"sync"

	"github.com/basecamp/once-campfire-go/internal/useragent"
)

type requestInfoKey struct{}

// One request-owned context value replaces separate host/origin values. Parsing
// stays lazy for non-browser endpoints, and derived contexts share the result.
type requestInfo struct {
	host, origin string
	agentOnce    sync.Once
	agent        *useragent.Agent
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
