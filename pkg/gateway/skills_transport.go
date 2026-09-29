package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// go-sdk v1.4.1 rejects unknown JSON-RPC methods before middleware runs, so
// the SEP-2640 methods are answered here, in front of the SDK.

func (g *Gateway) skillsResponse(req *jsonrpc.Request) *jsonrpc.Response {
	resp := &jsonrpc.Response{ID: req.ID}
	result, rpcErr := g.handleSkillsMethod(req.Method, req.Params)
	if rpcErr != nil {
		resp.Error = rpcErr
		return resp
	}
	raw, err := json.Marshal(result)
	if err != nil {
		resp.Error = &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: err.Error()}
		return resp
	}
	resp.Result = raw
	return resp
}

type skillsTransport struct {
	inner mcp.Transport
	g     *Gateway
}

func (t *skillsTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	conn, err := t.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &skillsConn{Connection: conn, g: t.g}, nil
}

type skillsConn struct {
	mcp.Connection
	g *Gateway
}

// Read answers skills methods inline and hands everything else to the SDK.
func (c *skillsConn) Read(ctx context.Context) (jsonrpc.Message, error) {
	for {
		msg, err := c.Connection.Read(ctx)
		if err != nil {
			return nil, err
		}
		req, ok := msg.(*jsonrpc.Request)
		if !ok || !req.IsCall() || !isSkillsMethod(req.Method) {
			return msg, nil
		}
		if err := c.Write(ctx, c.g.skillsResponse(req)); err != nil {
			return nil, err
		}
	}
}

// skillsHTTPHandler answers skills methods posted to the streaming endpoint
// and passes every other request to the SDK handler untouched.
func (g *Gateway) skillsHTTPHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if err != nil {
			http.Error(w, "reading request body", http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		var probe struct {
			Method string `json:"method"`
		}
		if json.Unmarshal(body, &probe) != nil || !isSkillsMethod(probe.Method) {
			next.ServeHTTP(w, r)
			return
		}
		msg, err := jsonrpc.DecodeMessage(body)
		req, ok := msg.(*jsonrpc.Request)
		if err != nil || !ok || !req.IsCall() {
			http.Error(w, "invalid JSON-RPC request", http.StatusBadRequest)
			return
		}
		out, err := jsonrpc.EncodeMessage(g.skillsResponse(req))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if sid := r.Header.Get("Mcp-Session-Id"); sid != "" {
			w.Header().Set("Mcp-Session-Id", sid)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(out)
	})
}
