package mcpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type testRoundTripper func(*http.Request) (*http.Response, error)

func (f testRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestTokenProviderIsSerializedAndCancelledRequestsSkipIt(t *testing.T) {
	var active, maximum, calls atomic.Int32
	source := &serializedTokenSource{provider: func(context.Context) (string, error) {
		n := active.Add(1)
		for old := maximum.Load(); n > old && !maximum.CompareAndSwap(old, n); old = maximum.Load() {
		}
		calls.Add(1)
		runtime.Gosched()
		active.Add(-1)
		return "token", nil
	}}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if token, err := source.accessToken(context.Background()); err != nil || token != "token" {
				t.Errorf("token=%q err=%v", token, err)
			}
		})
	}
	wg.Wait()
	if maximum.Load() != 1 || calls.Load() != 20 {
		t.Fatalf("maximum concurrent refresh=%d calls=%d", maximum.Load(), calls.Load())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := source.accessToken(ctx); !errors.Is(err, context.Canceled) || calls.Load() != 20 {
		t.Fatalf("cancelled source err=%v calls=%d", err, calls.Load())
	}
}

func TestDispatchGuardRunsAfterRefreshAndBeforeHTTP(t *testing.T) {
	var dispatched int
	valid := true
	invalid := errors.New("preview was invalidated")
	transport := bearerTransport{
		source: &serializedTokenSource{provider: func(context.Context) (string, error) { valid = false; return "new-token", nil }},
		base:   testRoundTripper(func(*http.Request) (*http.Response, error) { dispatched++; return nil, errors.New("must not dispatch") }),
	}
	ctx := WithDispatchGuard(context.Background(), func() error {
		if !valid {
			return invalid
		}
		return nil
	})
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.invalid/mcp", strings.NewReader("{}"))
	if _, err := transport.RoundTrip(request); !errors.Is(err, ErrNotDispatched) || !errors.Is(err, invalid) || dispatched != 0 {
		t.Fatalf("guard error=%v dispatched=%d", err, dispatched)
	}
	transport.source = &serializedTokenSource{provider: func(context.Context) (string, error) { return "", errors.New("refresh failed") }}
	if _, err := transport.RoundTrip(request); !errors.Is(err, ErrNotDispatched) || dispatched != 0 {
		t.Fatalf("refresh error=%v dispatched=%d", err, dispatched)
	}
	transport.source = &serializedTokenSource{provider: func(context.Context) (string, error) { return "", nil }}
	if _, err := transport.RoundTrip(request); !errors.Is(err, ErrNotDispatched) || dispatched != 0 {
		t.Fatalf("empty token error=%v dispatched=%d", err, dispatched)
	}
}

func TestRefreshableClientUsesLatestBearerWithoutStandaloneSSE(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	server.AddTool(&mcp.Tool{Name: "write", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ACTION COMPLETED"}}}, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	var token atomic.Value
	token.Store("initial-token")
	var mu sync.Mutex
	var authorization, methods []string
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		authorization = append(authorization, r.Header.Get("Authorization"))
		methods = append(methods, r.Method)
		mu.Unlock()
		handler.ServeHTTP(w, r)
	}))
	defer httpServer.Close()
	client, err := ConnectToWithTokenProvider(context.Background(), httpServer.URL, func(context.Context) (string, error) { return token.Load().(string), nil }, httpServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	token.Store("renewed-token")
	if _, err := client.ListTools(context.Background()); err != nil {
		t.Fatal(err)
	}
	if result, err := client.CallTool(context.Background(), "write", nil); err != nil || result.State != StateCommitted {
		t.Fatalf("write result=%#v err=%v", result, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(authorization) < 2 || authorization[0] != "Bearer initial-token" || authorization[len(authorization)-1] != "Bearer renewed-token" {
		t.Fatalf("authorization=%v", authorization)
	}
	for _, method := range methods {
		if method == http.MethodGet {
			t.Fatal("client opened a standalone SSE connection")
		}
	}
}

func TestClientNeverReplaysRejectedOrRedirectedWritePOST(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusInternalServerError, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
			handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
			var calls, redirected atomic.Int32
			httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/redirected" {
					redirected.Add(1)
				}
				var message struct{ Method string }
				if r.Body != nil {
					data, _ := io.ReadAll(r.Body)
					_ = json.Unmarshal(data, &message)
					r.Body = io.NopCloser(bytes.NewReader(data))
				}
				if message.Method == "tools/call" {
					calls.Add(1)
					w.Header().Set("Location", "/redirected")
					w.WriteHeader(status)
					return
				}
				handler.ServeHTTP(w, r)
			}))
			defer httpServer.Close()
			var tokenCalls atomic.Int32
			client, err := ConnectToWithTokenProvider(context.Background(), httpServer.URL, func(context.Context) (string, error) { tokenCalls.Add(1); return "token", nil }, httpServer.Client())
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			_, err = client.CallTool(context.Background(), "write", nil)
			if err == nil || calls.Load() != 1 || redirected.Load() != 0 {
				t.Fatalf("error=%v calls=%d redirects=%d", err, calls.Load(), redirected.Load())
			}
			if errors.Is(err, ErrUnauthorized) != (status == http.StatusUnauthorized) {
				t.Fatalf("HTTP %d authorization classification: %v", status, err)
			}
		})
	}
}

func TestClientResourceTemplatesPaginateUsingSDK(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, &mcp.ServerOptions{PageSize: 1})
	for _, name := range []string{"one", "two", "three"} {
		server.AddResourceTemplate(&mcp.ResourceTemplate{Name: name, URITemplate: name + ":///{id}"}, func(_ context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: request.Params.URI, Text: "template data"}}}, nil
		})
	}
	httpServer := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil))
	defer httpServer.Close()
	client, err := ConnectTo(context.Background(), httpServer.URL, "token", httpServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	templates, err := client.ListResourceTemplates(context.Background())
	if err != nil || len(templates) != 3 {
		t.Fatalf("templates=%#v err=%v", templates, err)
	}
	resource, err := client.ReadResource(context.Background(), "two:///42")
	if err != nil || resource.HumanText() != "template data" {
		t.Fatalf("resource=%#v err=%v", resource, err)
	}
}

func TestClientRejectsRepeatedPaginationCursors(t *testing.T) {
	for _, method := range []string{"tools/list", "resources/list", "resources/templates/list"} {
		t.Run(method, func(t *testing.T) {
			server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
			handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
			var calls atomic.Int32
			httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var message struct {
					Method string          `json:"method"`
					ID     json.RawMessage `json:"id"`
				}
				if r.Body != nil {
					data, _ := io.ReadAll(r.Body)
					_ = json.Unmarshal(data, &message)
					r.Body = io.NopCloser(bytes.NewReader(data))
				}
				if message.Method == method {
					calls.Add(1)
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": map[string]any{"nextCursor": "same", "tools": []any{}, "resources": []any{}, "resourceTemplates": []any{}}})
					return
				}
				handler.ServeHTTP(w, r)
			}))
			defer httpServer.Close()
			client, err := ConnectTo(context.Background(), httpServer.URL, "token", httpServer.Client())
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			switch method {
			case "tools/list":
				_, err = client.ListTools(context.Background())
			case "resources/list":
				_, err = client.ListResources(context.Background())
			default:
				_, err = client.ListResourceTemplates(context.Background())
			}
			if err == nil || !strings.Contains(err.Error(), "repeated pagination cursor") || calls.Load() != 2 {
				t.Fatalf("pagination err=%v calls=%d", err, calls.Load())
			}
		})
	}
}

func TestClientRequiresNonNilProvider(t *testing.T) {
	if _, err := ConnectToWithTokenProvider(context.Background(), "https://example.invalid/mcp", nil, nil); err == nil {
		t.Fatal("nil provider accepted")
	}
}
