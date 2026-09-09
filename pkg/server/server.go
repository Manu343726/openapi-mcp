package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	// "fmt" // No longer needed here
	// "sync" // No longer needed here

	"github.com/ckanthony/openapi-mcp/pkg/config"
	"github.com/ckanthony/openapi-mcp/pkg/mcp"
	"github.com/google/uuid" // Import UUID package
)

// --- JSON-RPC Structures (Re-introduced for Handshake/Messages) ---

type jsonRPCRequest struct {
	Jsonrpc string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
	ID      interface{} `json:"id,omitempty"` // Can be string, number, or null
}

type jsonRPCResponse struct {
	Jsonrpc string      `json:"jsonrpc"`
	Result  interface{} `json:"result,omitempty"`
	Error   *jsonError  `json:"error,omitempty"`
	ID      interface{} `json:"id,omitempty"` // Omitted for server->client notifications
	// Method/Params are populated for server->client notifications (e.g.
	// notifications/tools/list_changed), which carry no id.
	Method string      `json:"method,omitempty"`
	Params interface{} `json:"params,omitempty"`
}

type jsonError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// --- MCP Message Structures (Kept for clarity on expected payloads) ---

// MCPMessage represents a generic message exchanged over the transport.
// Note: Adapt this structure based on the exact MCP spec requirements if needed.
// This structure is now more for understanding the *payloads* within JSON-RPC.
type MCPMessage struct {
	Type    string          `json:"type"`                   // e.g., "initialize", "tools/list", "tools/call", "tool_result", "error"
	ID      string          `json:"id,omitempty"`           // Unique message ID (less relevant for JSON-RPC wrapper)
	Payload json.RawMessage `json:"payload,omitempty"`      // Content specific to the message type
	ConnID  string          `json:"connectionId,omitempty"` // Included in responses related to a connection
}

// MCPError defines a structured error for MCP responses.
// This will be used within the 'Error.Data' field of a jsonRPCResponse.
type MCPError struct {
	Code    int         `json:"code,omitempty"` // Optional error code
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"` // Optional additional data
}

// ToolCallParams represents the expected payload for a tools/call request.
// This will be the structure within the 'params' field of a jsonRPCRequest.
type ToolCallParams struct {
	ToolName string                 `json:"name"`      // Aligning with gin-mcp JSON-RPC 'name'
	Input    map[string]interface{} `json:"arguments"` // Aligning with gin-mcp JSON-RPC 'arguments'
}

// ToolResultContent represents an item in the 'content' array of a tool_result.
type ToolResultContent struct {
	Type string `json:"type"`
	Text string `json:"text"` // Assuming text/JSON string result
	// Add other content types if needed
}

// ToolResultPayload represents the structure for the 'result' of a 'tool_result' JSON-RPC response.
type ToolResultPayload struct {
	Content    []ToolResultContent `json:"content"`                // Array of content items (always populated)
	IsError    bool                `json:"isError,omitempty"`      // Optional: true if error occurred
	StatusCode int                 `json:"statusCode,omitempty"`   // HTTP status code from API call
	Error      *MCPError           `json:"error,omitempty"`        // Detailed error info if IsError is true
	ToolCallID string              `json:"tool_call_id,omitempty"` // Optional: Can be helpful
}

// --- Server State ---

// activeConnections stores channels for sending messages back to active SSE clients.
var activeConnections = make(map[string]chan jsonRPCResponse) // Changed value type
var connMutex sync.RWMutex

// initializedConnections tracks which sessions completed the MCP 'initialize'
// handshake. tools/list_changed notifications are only meaningful to them.
var initializedConnections = make(map[string]bool)

// Channel buffer size
const messageChannelBufferSize = 10

// --- Server Implementation ---

// ServeMCP starts an HTTP server handling MCP communication for a Registry.
func ServeMCP(addr string, reg *Registry) error {
	if reg == nil {
		return fmt.Errorf("registry is required")
	}
	log.Printf("Preparing registry with %d API(s) for MCP...", len(reg.APIs()))

	// --- Handler Functions ---
	mcpHandler := func(w http.ResponseWriter, r *http.Request) {
		// CORS Headers (Apply to all relevant requests)
		w.Header().Set("Access-Control-Allow-Origin", "*") // Be more specific in production
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, accept, origin, Cache-Control, X-Requested-With, X-Connection-ID")
		w.Header().Set("Access-Control-Expose-Headers", "X-Connection-ID")

		if r.Method == http.MethodOptions {
			log.Println("Responding to OPTIONS request")
			w.WriteHeader(http.StatusNoContent) // Use 204 No Content for OPTIONS
			return
		}

		if r.Method == http.MethodGet {
			httpMethodGetHandler(w, r, reg) // Handle SSE connection setup
		} else if r.Method == http.MethodPost {
			httpMethodPostHandler(w, r, reg)
		} else {
			log.Printf("Method Not Allowed: %s", r.Method)
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		}
	}

	// Setup server mux
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", mcpHandler) // Single endpoint for GET/POST/OPTIONS

	log.Printf("MCP server listening on %s/mcp", addr)
	return http.ListenAndServe(addr, mux)
}

// httpMethodGetHandler handles the initial GET request to establish the SSE connection.
func httpMethodGetHandler(w http.ResponseWriter, r *http.Request, regs ...*Registry) {
	connectionID := uuid.New().String()
	log.Printf("SSE client connecting: %s (Assigning ID: %s)", r.RemoteAddr, connectionID)

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported!", http.StatusInternalServerError)
		log.Println("Error: Client connection does not support flushing")
		return
	}

	// --- Set headers FIRST ---
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// CORS headers are set in the main handler
	w.Header().Set("X-Connection-ID", connectionID)
	w.Header().Set("X-Accel-Buffering", "no") // Useful for proxies like Nginx
	w.WriteHeader(http.StatusOK)              // Write headers and status code
	flusher.Flush()                           // Ensure headers are sent immediately

	// --- Send initial :ok --- (Must happen *after* headers)
	if _, err := fmt.Fprintf(w, ":ok\n\n"); err != nil {
		log.Printf("Error sending SSE preamble to %s (ID: %s): %v", r.RemoteAddr, connectionID, err)
		return // Cannot proceed if preamble fails
	}
	flusher.Flush()
	log.Printf("Sent :ok preamble to %s (ID: %s)", r.RemoteAddr, connectionID)

	// --- Send initial SSE events --- (endpoint, mcp-ready)
	endpointURL := fmt.Sprintf("/mcp?sessionId=%s", connectionID) // Assuming /mcp is the mount path
	if err := writeSSEEvent(w, "endpoint", endpointURL); err != nil {
		log.Printf("Error sending SSE endpoint event to %s (ID: %s): %v", r.RemoteAddr, connectionID, err)
		return
	}
	flusher.Flush()
	log.Printf("Sent endpoint event to %s (ID: %s)", r.RemoteAddr, connectionID)

	readyMsg := jsonRPCRequest{ // Use request struct for notification format
		Jsonrpc: "2.0",
		Method:  "mcp-ready",
		Params: map[string]interface{}{ // Put data in params
			"connectionId": connectionID,
			"status":       "connected",
			"protocol":     "2.0",
		},
	}
	if err := writeSSEEvent(w, "message", readyMsg); err != nil {
		log.Printf("Error sending SSE mcp-ready event to %s (ID: %s): %v", r.RemoteAddr, connectionID, err)
		return
	}
	flusher.Flush()
	log.Printf("Sent mcp-ready event to %s (ID: %s)", r.RemoteAddr, connectionID)

	// --- Setup message channel and store connection ---
	msgChan := make(chan jsonRPCResponse, messageChannelBufferSize) // Channel for responses
	connMutex.Lock()
	activeConnections[connectionID] = msgChan
	connMutex.Unlock()
	log.Printf("Registered channel for connection %s. Active connections: %d", connectionID, len(activeConnections))

	cleanup := func() {
		connMutex.Lock()
		delete(activeConnections, connectionID)
		delete(initializedConnections, connectionID)
		connMutex.Unlock()
		if len(regs) > 0 && regs[0] != nil {
			regs[0].DropSession(connectionID)
		}
		close(msgChan) // Close channel when connection ends
		log.Printf("Removed connection %s. Active connections: %d", connectionID, len(activeConnections))
	}
	defer cleanup()

	// --- Goroutine to write messages from channel to SSE stream ---
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	go func() {
		log.Printf("[SSE Writer %s] Starting message writer goroutine", connectionID)
		defer log.Printf("[SSE Writer %s] Exiting message writer goroutine", connectionID)
		for {
			select {
			case <-ctx.Done():
				return // Exit if main context is cancelled
			case resp, ok := <-msgChan:
				if !ok {
					log.Printf("[SSE Writer %s] Message channel closed.", connectionID)
					return // Exit if channel is closed
				}
				log.Printf("[SSE Writer %s] Sending message (ID: %v) via SSE", connectionID, resp.ID)
				if err := writeSSEEvent(w, "message", resp); err != nil {
					log.Printf("[SSE Writer %s] Error writing message to SSE stream: %v. Cancelling context.", connectionID, err)
					cancel() // Signal main loop to exit on write error
					return
				}
				flusher.Flush() // Flush after writing message
			}
		}
	}()

	// --- Keep connection alive (main loop) ---
	keepAliveTicker := time.NewTicker(20 * time.Second)
	defer keepAliveTicker.Stop()

	log.Printf("[SSE %s] Entering keep-alive loop", connectionID)
	for {
		select {
		case <-ctx.Done():
			log.Printf("[SSE %s] Context done. Exiting keep-alive loop.", connectionID)
			return // Exit loop if context cancelled (client disconnect or write error)
		case <-keepAliveTicker.C:
			// Send JSON-RPC ping notification instead of SSE comment
			pingMsg := jsonRPCRequest{ // Use request struct for notification format
				Jsonrpc: "2.0",
				Method:  "ping",
				Params: map[string]interface{}{ // Include timestamp like gin-mcp
					"timestamp": time.Now().Unix(),
				},
			}
			if err := writeSSEEvent(w, "message", pingMsg); err != nil {
				log.Printf("[SSE %s] Error sending ping notification: %v. Closing connection.", connectionID, err)
				cancel() // Signal writer goroutine and exit
				return
			}
			flusher.Flush()
		}
	}
}

// writeSSEEvent formats and writes data as a Server-Sent Event.
func writeSSEEvent(w http.ResponseWriter, eventName string, data interface{}) error {
	buffer := bytes.Buffer{}
	if eventName != "" {
		buffer.WriteString(fmt.Sprintf("event: %s\n", eventName))
	}

	// Marshal data to JSON if it's not a simple string already
	var dataStr string
	if strData, ok := data.(string); ok && eventName == "endpoint" { // Special case for endpoint URL
		dataStr = strData
	} else {
		jsonData, err := json.Marshal(data)
		if err != nil {
			return fmt.Errorf("failed to marshal data for SSE event '%s': %w", eventName, err)
		}
		dataStr = string(jsonData)
	}

	// Write data line(s). Split multiline JSON for proper SSE formatting.
	lines := strings.Split(dataStr, "\n")
	for _, line := range lines {
		buffer.WriteString(fmt.Sprintf("data: %s\n", line))
	}

	// Add final newline
	buffer.WriteString("\n")

	// Write to the response writer
	_, err := w.Write(buffer.Bytes())
	if err != nil {
		return fmt.Errorf("failed to write SSE event '%s': %w", eventName, err)
	}
	return nil
}

// httpMethodPostHandler handles incoming POST requests containing MCP messages.
// It supports two transports:
//   - legacy SSE: POSTs carry an X-Connection-ID (or sessionId) established by
//     a streaming GET; responses are queued to that connection's SSE channel.
//   - streamable HTTP: a POST without a connection id is handled synchronously
//     and answered with a JSON-RPC body (this is what modern clients such as
//     opencode use).
func httpMethodPostHandler(w http.ResponseWriter, r *http.Request, reg *Registry) {
	connID := r.Header.Get("X-Connection-ID") // Try header first
	if connID == "" {
		connID = r.URL.Query().Get("sessionId") // Fallback to query parameter
		log.Printf("X-Connection-ID header missing, checking sessionId query param: found='%s'", connID)
	}

	if connID == "" {
		handleStreamableRequest(w, r, reg)
		return
	}

	// Find the corresponding message channel for this connection
	connMutex.RLock()
	msgChan, isActive := activeConnections[connID]
	connMutex.RUnlock()

	if !isActive {
		log.Printf("Error: POST request received for inactive/unknown connection ID: %s", connID)
		// Still send sync error here, as we don't have a channel
		tryWriteHTTPError(w, http.StatusNotFound, "Invalid or expired connection ID")
		return
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("Error reading POST request body for %s: %v", connID, err)
		// Create error response in the ToolResultPayload format
		errPayload := ToolResultPayload{
			IsError: true,
			Error: &MCPError{
				Code:    -32700, // JSON-RPC Parse Error Code
				Message: "Parse error reading request body",
			},
			// ToolCallID doesn't really apply here, maybe use connID or leave empty?
			// ToolCallID: connID,
		}
		errResp := jsonRPCResponse{
			Jsonrpc: "2.0",
			ID:      nil, // ID is unknown if we can't read the body
			Result:  errPayload,
			Error:   nil, // Ensure top-level error is nil
		}
		// Attempt to send via SSE channel
		select {
		case msgChan <- errResp:
			log.Printf("Queued read error response (ID: %v) for %s onto SSE channel (as Result)", errResp.ID, connID)
			// Send HTTP 202 Accepted back to the POST request
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprintln(w, "Request accepted (with parse error), response will be sent via SSE.")
		default:
			log.Printf("Error: Failed to queue read error response (ID: %v) for %s - SSE channel likely full or closed.", errResp.ID, connID)
			// Send an error back on the POST request if channel fails
			tryWriteHTTPError(w, http.StatusInternalServerError, "Failed to queue error response for SSE channel")
		}
		return // Stop processing
	}
	// No defer r.Body.Close() needed here as io.ReadAll reads to EOF

	log.Printf("Received POST data for %s: %s", connID, string(bodyBytes))

	// Attempt to unmarshal into a temporary map first to extract ID if possible
	var rawReq map[string]interface{}
	var reqID interface{} // Keep track of ID even if full unmarshal fails

	// Try unmarshalling into raw map
	if err := json.Unmarshal(bodyBytes, &rawReq); err == nil {
		// Ensure reqID is treated as a string or number if possible, handle potential null
		if idVal, idExists := rawReq["id"]; idExists && idVal != nil {
			reqID = idVal
		} else {
			reqID = nil // Explicitly set to nil if missing or JSON null
		}
	} else {
		// Full unmarshal failed, log it but continue to try specific struct
		log.Printf("Warning: Initial unmarshal into map failed for %s: %v. Will attempt specific struct unmarshal.", connID, err)
		reqID = nil // ID is unknown
	}

	var req jsonRPCRequest // Expect JSON-RPC request
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		log.Printf("Error decoding JSON-RPC request for %s: %v", connID, err)
		// Use createJSONRPCError to correctly format the error response
		errResp := createJSONRPCError(reqID, -32700, "Parse error decoding JSON request", err.Error())

		// Attempt to send via SSE channel
		select {
		case msgChan <- errResp:
			log.Printf("Queued decode error response (ID: %v) for %s onto SSE channel", errResp.ID, connID)
			// Send HTTP 202 Accepted back to the POST request
			w.WriteHeader(http.StatusAccepted)
			// Use a specific message for decode errors
			fmt.Fprintln(w, "Request accepted (with decode error), response will be sent via SSE.")
		default:
			log.Printf("Error: Failed to queue decode error response (ID: %v) for %s - SSE channel likely full or closed.", errResp.ID, connID)
			// Send an error back on the POST request if channel fails
			tryWriteHTTPError(w, http.StatusInternalServerError, "Failed to queue error response for SSE channel")
		}
		return // Stop processing
	}

	// If we successfully unmarshalled 'req', ensure reqID matches req.ID
	if req.ID != nil {
		reqID = req.ID
	} else {
		reqID = nil
	}

	// --- Variable to hold the final response to be sent via SSE ---
	var respToSend jsonRPCResponse

	// --- Validate JSON-RPC Request & dispatch ---
	var isNotification bool
	respToSend, isNotification = dispatchJSONRPC(connID, &req, reqID, reg)

	if isNotification {
		log.Printf("Notification %q received for %s. Ignoring.", req.Method, connID)
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintln(w, "Notification received.")
		return
	}

	// --- Send response ASYNCHRONOUSLY via SSE channel (unless handled earlier) ---
	select {
	case msgChan <- respToSend:
		log.Printf("Queued response (ID: %v) for %s onto SSE channel", respToSend.ID, connID)
		// Send HTTP 202 Accepted back to the POST request
		w.WriteHeader(http.StatusAccepted)
		// Use the standard message for successfully queued responses
		fmt.Fprintln(w, "Request accepted, response will be sent via SSE.")
	default:
		log.Printf("Error: Failed to queue response (ID: %v) for %s - SSE channel likely full or closed.", respToSend.ID, connID)
		http.Error(w, "Failed to queue response for SSE channel", http.StatusInternalServerError)
	}
}

// dispatchJSONRPC validates a parsed JSON-RPC request and runs it against the
// registry, returning the response to send. It returns isNotification=true for
// requests that have no response (e.g. notifications/initialized).
func dispatchJSONRPC(connID string, req *jsonRPCRequest, reqID interface{}, reg *Registry) (jsonRPCResponse, bool) {
	if req.Jsonrpc != "2.0" {
		log.Printf("Invalid JSON-RPC version ('%s') for %s, ID: %v", req.Jsonrpc, connID, reqID)
		return createJSONRPCError(reqID, -32600, "Invalid Request: jsonrpc field must be \"2.0\"", nil), false
	}
	if req.Method == "" {
		log.Printf("Missing JSON-RPC method for %s, ID: %v", connID, reqID)
		return createJSONRPCError(reqID, -32600, "Invalid Request: method field is missing or empty", nil), false
	}

	log.Printf("Processing JSON-RPC message for %s: Method=%s, ID=%v", connID, req.Method, reqID)
	switch req.Method {
	case "initialize":
		incomingInitializeJSON, _ := json.Marshal(req)
		log.Printf("DEBUG: Handling 'initialize' for %s. Incoming request: %s", connID, string(incomingInitializeJSON))
		markConnectionInitialized(connID)
		resp := handleInitializeJSONRPC(connID, req)
		outgoingInitializeJSON, _ := json.Marshal(resp)
		log.Printf("DEBUG: Prepared 'initialize' response for %s. Outgoing response: %s", connID, string(outgoingInitializeJSON))
		return resp, false
	case "notifications/initialized":
		return jsonRPCResponse{}, true // no response to send
	case "logging/setLevel":
		return handleLoggingSetLevelJSONRPC(connID, req), false
	case "tools/list":
		return handleToolsListJSONRPC(connID, req, reg), false
	case "tools/call":
		return handleToolCallJSONRPC(connID, req, reg), false
	case "ping":
		return jsonRPCResponse{Jsonrpc: "2.0", ID: req.ID, Result: map[string]interface{}{}}, false
	default:
		log.Printf("Received unknown JSON-RPC method '%s' for %s", req.Method, connID)
		return createJSONRPCError(reqID, -32601, fmt.Sprintf("Method not found: %s", req.Method), nil), false
	}
}

// handleStreamableRequest serves a single JSON-RPC POST synchronously, the way
// the MCP streamable HTTP transport works (a JSON body in, a JSON body out; no
// separate SSE session needed).
func handleStreamableRequest(w http.ResponseWriter, r *http.Request, reg *Registry) {
	if r.Body == nil {
		http.Error(w, "empty body", http.StatusBadRequest)
		return
	}
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	// Support both single requests and JSON-RPC batches.
	var raw interface{}
	if err := json.Unmarshal(bodyBytes, &raw); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(createJSONRPCError(nil, -32700, "Parse error decoding JSON request", err.Error()))
		return
	}

	respond := func(resp jsonRPCResponse) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}

	if arr, ok := raw.([]interface{}); ok {
		// Batch: respond with an array of results.
		var responses []jsonRPCResponse
		for _, item := range arr {
			resp, isNotification := dispatchRawItem(r, item, reg)
			if !isNotification {
				responses = append(responses, resp)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(responses)
		return
	}

	resp, isNotification := dispatchRawItem(r, raw, reg)
	if isNotification {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	respond(resp)
}

// dispatchRawItem unmarshals one JSON-RPC request object and dispatches it.
func dispatchRawItem(r *http.Request, item interface{}, reg *Registry) (jsonRPCResponse, bool) {
	reqBytes, err := json.Marshal(item)
	if err != nil {
		return createJSONRPCError(nil, -32600, "Invalid request", nil), false
	}
	var reqID interface{}
	if m, ok := item.(map[string]interface{}); ok {
		if idVal, has := m["id"]; has && idVal != nil {
			reqID = idVal
		}
	}
	var req jsonRPCRequest
	if err := json.Unmarshal(reqBytes, &req); err != nil {
		return createJSONRPCError(reqID, -32600, "Invalid Request", err.Error()), false
	}
	return dispatchJSONRPC("", &req, reqID, reg)
}

// --- JSON-RPC Message Handlers --- // Implementations returning jsonRPCResponse

func handleInitializeJSONRPC(connID string, req *jsonRPCRequest) jsonRPCResponse {
	log.Printf("Handling 'initialize' (JSON-RPC) for %s", connID)

	// Honor the protocol version the client asked for if we support it.
	protocolVersion := "2024-11-05"
	if params, ok := req.Params.(map[string]interface{}); ok {
		if v, ok := params["protocolVersion"].(string); ok && v != "" {
			protocolVersion = v
		}
	}

	// Construct the result payload based on gin-mcp's structure using map[string]interface{}
	resultPayload := map[string]interface{}{
		"protocolVersion": protocolVersion,
		"capabilities": map[string]interface{}{
			"tools": map[string]interface{}{
				"enabled": true,
				"config": map[string]interface{}{
					"listChanged": true,
				},
			},
			"prompts": map[string]interface{}{
				"enabled": false,
			},
			"resources": map[string]interface{}{
				"enabled": true,
			},
			"logging": map[string]interface{}{
				"enabled": false,
			},
			"roots": map[string]interface{}{
				"listChanged": false,
			},
		},
		"serverInfo": map[string]interface{}{
			"name":    "OpenAPI-MCP",       // Or use config name if available
			"version": "openapi-mcp-0.1.0", // Your server version
		},
		"connectionId": connID, // Include the connection ID
	}

	return jsonRPCResponse{
		Jsonrpc: "2.0",
		ID:      req.ID, // Match request ID
		Result:  resultPayload,
	}
}

func handleToolsListJSONRPC(connID string, req *jsonRPCRequest, reg *Registry) jsonRPCResponse {
	log.Printf("Handling 'tools/list' (JSON-RPC) for %s", connID)

	// Construct the result payload based on gin-mcp's structure
	resultPayload := map[string]interface{}{
		"tools": reg.Tools(),
		"metadata": map[string]interface{}{
			"version": "2024-11-05", // Align with gin-mcp if possible
			"count":   len(reg.Tools()),
		},
	}

	return jsonRPCResponse{
		Jsonrpc: "2.0",
		ID:      req.ID, // Match request ID
		Result:  resultPayload,
	}
}

func handleLoggingSetLevelJSONRPC(connID string, req *jsonRPCRequest) jsonRPCResponse {
	log.Printf("Handling 'logging/setLevel' (JSON-RPC) for %s", connID)

	return jsonRPCResponse{
		Jsonrpc: "2.0",
		ID:      req.ID,
		Result:  map[string]interface{}{},
	}
}

// executeToolCall performs the actual HTTP request based on the resolved operation and parameters.
// It now correctly handles API key injection based on the *cfg* parameter.
func buildToolRequest(params *ToolCallParams, toolSet *mcp.ToolSet, cfg *config.Config) (*http.Request, error) {
	toolName := params.ToolName
	toolInput := params.Input // This is the map[string]interface{} from the client

	log.Printf("[ExecuteToolCall] Looking up details for tool: %s", toolName)
	operation, ok := toolSet.Operations[toolName]
	if !ok {
		log.Printf("[ExecuteToolCall] Error: Operation details not found for tool '%s'", toolName)
		return nil, fmt.Errorf("operation details for tool '%s' not found", toolName)
	}
	log.Printf("[ExecuteToolCall] Found operation: Method=%s, Path=%s", operation.Method, operation.Path)

	// --- Resolve API Key (using cfg passed from main) ---
	resolvedKey := cfg.GetAPIKey()
	apiKeyName := cfg.APIKeyName
	apiKeyLocation := cfg.APIKeyLocation
	hasServerKey := resolvedKey != "" && apiKeyName != "" && apiKeyLocation != ""

	log.Printf("[ExecuteToolCall] API Key Details: Name='%s', In='%s', HasServerValue=%t", apiKeyName, apiKeyLocation, resolvedKey != "")

	// --- Prepare Request Components ---
	baseURL := operation.BaseURL // Use BaseURL from the specific operation
	if cfg.ServerBaseURL != "" {
		baseURL = cfg.ServerBaseURL // Override if global base URL is set
		log.Printf("[ExecuteToolCall] Overriding base URL with global config: %s", baseURL)
	}
	if baseURL == "" {
		log.Printf("[ExecuteToolCall] Warning: No base URL found for operation %s and no global override set.", toolName)
		// For now, assume relative if empty.
	}

	path := operation.Path
	queryParams := url.Values{}
	pathParams := make(map[string]string)
	headerParams := make(http.Header)        // For headers to add
	cookieParams := []*http.Cookie{}         // For cookies to add
	bodyData := make(map[string]interface{}) // For building the request body
	requestBodyRequired := operation.Method == "POST" || operation.Method == "PUT" || operation.Method == "PATCH"

	// Create a map of expected parameters from the operation details for easier lookup
	expectedParams := make(map[string]string) // Map param name to its location ('in')
	for _, p := range operation.Parameters {
		expectedParams[p.Name] = p.In
	}

	// --- Process Input Parameters (Separating and Handling API Key Override) ---
	log.Printf("[ExecuteToolCall] Processing %d input parameters...", len(toolInput))
	for key, value := range toolInput {
		// --- API Key Override Check ---
		// If this input param is the API key AND we have a valid server key config,
		// skip processing the client's value entirely.
		if hasServerKey && key == apiKeyName {
			log.Printf("[ExecuteToolCall] Skipping client-provided param '%s' due to server API key override.", key)
			continue
		}
		// --- End API Key Override ---

		paramLocation, knownParam := expectedParams[key]
		pathPlaceholder := "{" + key + "}" // OpenAPI uses {param}

		if strings.Contains(path, pathPlaceholder) {
			// Handle path parameter substitution
			pathParams[key] = fmt.Sprintf("%v", value)
			log.Printf("[ExecuteToolCall] Found path parameter %s=%v", key, value)
		} else if knownParam {
			// Handle parameters defined in the spec (query, header, cookie)
			switch paramLocation {
			case "query":
				queryParams.Add(key, fmt.Sprintf("%v", value))
				log.Printf("[ExecuteToolCall] Found query parameter %s=%v (from spec)", key, value)
			case "header":
				headerParams.Add(key, fmt.Sprintf("%v", value))
				log.Printf("[ExecuteToolCall] Found header parameter %s=%v (from spec)", key, value)
			case "cookie":
				cookieParams = append(cookieParams, &http.Cookie{Name: key, Value: fmt.Sprintf("%v", value)})
				log.Printf("[ExecuteToolCall] Found cookie parameter %s=%v (from spec)", key, value)
			// case "formData": // TODO: Handle form data if needed
			// 	bodyData[key] = value // Or handle differently based on content type
			// 	log.Printf("[ExecuteToolCall] Found formData parameter %s=%v (from spec)", key, value)
			default:
				// Known parameter but location handling is missing or mismatched.
				if paramLocation == "path" && (operation.Method == "GET" || operation.Method == "DELETE") {
					// If spec says 'path' but it wasn't in the actual path, and it's a GET/DELETE,
					// treat it as a query parameter as a fallback.
					log.Printf("[ExecuteToolCall] Warning: Parameter '%s' is 'path' in spec but not in URL path '%s'. Adding to query parameters as fallback for GET/DELETE.", key, operation.Path)
					queryParams.Add(key, fmt.Sprintf("%v", value))
				} else {
					// Otherwise, log the warning and ignore.
					log.Printf("[ExecuteToolCall] Warning: Parameter '%s' has unsupported or unhandled location '%s' in spec. Ignoring.", key, paramLocation)
				}
			}
		} else if requestBodyRequired {
			// If parameter is not in path or defined in spec params, and method expects a body,
			// assume it belongs in the request body.
			bodyData[key] = value
			log.Printf("[ExecuteToolCall] Added body parameter %s=%v (assumed)", key, value)
		} else {
			// Parameter not in path, not in spec, and not a body method.
			// This could be an extraneous parameter like 'explanation'. Log it.
			log.Printf("[ExecuteToolCall] Ignoring parameter '%s' as it doesn't match path or known parameter location for method %s.", key, operation.Method)
		}
	}

	// --- Substitute Path Parameters ---
	for key, value := range pathParams {
		path = strings.Replace(path, "{"+key+"}", value, -1)
	}

	// --- Inject Server API Key (if applicable) ---
	if hasServerKey {
		log.Printf("[ExecuteToolCall] Injecting server API key (Name: %s, Location: %s)", apiKeyName, string(apiKeyLocation))
		switch apiKeyLocation {
		case config.APIKeyLocationQuery:
			queryParams.Set(apiKeyName, resolvedKey) // Set overrides any previous value
			log.Printf("[ExecuteToolCall] Injected API key '%s' into query parameters", apiKeyName)
		case config.APIKeyLocationHeader:
			headerParams.Set(apiKeyName, resolvedKey) // Set overrides any previous value
			log.Printf("[ExecuteToolCall] Injected API key '%s' into headers", apiKeyName)
		case config.APIKeyLocationPath:
			pathPlaceholder := "{" + apiKeyName + "}"
			if strings.Contains(path, pathPlaceholder) {
				path = strings.Replace(path, pathPlaceholder, resolvedKey, -1)
				log.Printf("[ExecuteToolCall] Injected API key into path parameter '%s'", apiKeyName)
			} else {
				log.Printf("[ExecuteToolCall] Warning: API key location is 'path' but placeholder '%s' not found in final path '%s' for injection.", pathPlaceholder, path)
			}
		case config.APIKeyLocationCookie:
			// Check if cookie already exists from input, replace if so
			foundCookie := false
			for i, c := range cookieParams {
				if c.Name == apiKeyName {
					log.Printf("[ExecuteToolCall] Replacing existing cookie '%s' with injected API key.", apiKeyName)
					cookieParams[i] = &http.Cookie{Name: apiKeyName, Value: resolvedKey} // Replace existing
					foundCookie = true
					break
				}
			}
			if !foundCookie {
				log.Printf("[ExecuteToolCall] Adding new cookie '%s' with injected API key.", apiKeyName)
				cookieParams = append(cookieParams, &http.Cookie{Name: apiKeyName, Value: resolvedKey}) // Append new
			}
		default:
			// Use log.Printf for consistency
			log.Printf("Warning: Unsupported API key location specified in config: '%s'", apiKeyLocation)
		}
	} else {
		log.Printf("[ExecuteToolCall] Skipping server API key injection (config incomplete or key unresolved).")
	}

	// --- Inject Session Token (login-derived, if present) ---
	if cfg.SessionToken != "" {
		token := cfg.SessionTokenPrefix + cfg.SessionToken
		switch cfg.SessionTokenLocation {
		case config.APIKeyLocationQuery:
			queryParams.Set(cfg.SessionTokenName, token)
			log.Printf("[ExecuteToolCall] Injected session token '%s' into query parameters", cfg.SessionTokenName)
		case config.APIKeyLocationCookie:
			foundCookie := false
			for i, c := range cookieParams {
				if c.Name == cfg.SessionTokenName {
					cookieParams[i] = &http.Cookie{Name: cfg.SessionTokenName, Value: token}
					foundCookie = true
					break
				}
			}
			if !foundCookie {
				cookieParams = append(cookieParams, &http.Cookie{Name: cfg.SessionTokenName, Value: token})
			}
			log.Printf("[ExecuteToolCall] Injected session token into cookie '%s'", cfg.SessionTokenName)
		default: // header
			headerParams.Set(cfg.SessionTokenName, token)
			log.Printf("[ExecuteToolCall] Injected session token '%s' into headers", cfg.SessionTokenName)
		}
	}

	// --- Final URL Construction ---
	// Reconstruct query string *after* potential API key injection
	targetURL := baseURL + path
	if len(queryParams) > 0 {
		targetURL += "?" + queryParams.Encode()
	}
	log.Printf("[ExecuteToolCall] Final Target URL: %s %s", operation.Method, targetURL)

	// --- Prepare Request Body ---
	var reqBody io.Reader
	var bodyBytes []byte // Keep for logging
	if requestBodyRequired && len(bodyData) > 0 {
		var err error
		bodyBytes, err = json.Marshal(bodyData)
		if err != nil {
			log.Printf("[ExecuteToolCall] Error marshalling request body: %v", err)
			return nil, fmt.Errorf("error marshalling request body: %w", err)
		}
		reqBody = bytes.NewBuffer(bodyBytes)
		log.Printf("[ExecuteToolCall] Request body: %s", string(bodyBytes))
	}

	// --- Create HTTP Request ---
	req, err := http.NewRequest(operation.Method, targetURL, reqBody)
	if err != nil {
		log.Printf("[ExecuteToolCall] Error creating HTTP request: %v", err)
		return nil, fmt.Errorf("error creating request: %w", err)
	}

	// --- Set Headers ---
	// Default headers
	req.Header.Set("Accept", "application/json") // Assume JSON response typical for APIs
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json") // Assume JSON body if body exists
	}

	// Add headers collected from input/spec AND potentially injected API key
	for key, values := range headerParams {
		// Note: We use Set, assuming single value per header from input typically.
		// If multi-value headers are needed from spec/input, use Add.
		if len(values) > 0 {
			req.Header.Set(key, values[0])
		}
	}

	// Add custom headers from config (comma-separated)
	if cfg.CustomHeaders != "" {
		headers := strings.Split(cfg.CustomHeaders, ",")
		for _, h := range headers {
			parts := strings.SplitN(h, ":", 2)
			if len(parts) == 2 {
				headerName := strings.TrimSpace(parts[0])
				headerValue := strings.TrimSpace(parts[1])
				if headerName != "" {
					req.Header.Set(headerName, headerValue) // Set overrides potential input
					log.Printf("[ExecuteToolCall] Added custom header from config: %s", headerName)
				}
			}
		}
	}

	// --- Add Cookies ---
	for _, cookie := range cookieParams {
		req.AddCookie(cookie)
	}

	log.Printf("[ExecuteToolCall] Prepared request with headers: %v", req.Header)
	if len(req.Cookies()) > 0 {
		log.Printf("[ExecuteToolCall] Request cookies: %+v", req.Cookies())
	}

	return req, nil
}

// executeToolCall builds an *http.Request and sends it.
func executeToolCall(params *ToolCallParams, toolSet *mcp.ToolSet, cfg *config.Config) (*http.Response, error) {
	req, err := buildToolRequest(params, toolSet, cfg)
	if err != nil {
		return nil, err
	}
	log.Printf("[ExecuteToolCall] Sending request with headers: %v", req.Header)
	client := httpClientForConfig(cfg)
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[ExecuteToolCall] Error executing HTTP request: %v", err)
		return nil, fmt.Errorf("error executing request: %w", err)
	}
	log.Printf("[ExecuteToolCall] Request executed. Status Code: %d", resp.StatusCode)
	// Note: Don't close resp.Body here, the caller (handleToolCallJSONRPC) needs it.
	return resp, nil
}

func handleToolCallJSONRPC(connID string, req *jsonRPCRequest, reg *Registry) jsonRPCResponse {
	// req.Params is interface{}, but should contain json.RawMessage for tools/call
	rawParams, ok := req.Params.(json.RawMessage)
	if !ok {
		// If it's not RawMessage, maybe it was already decoded to a map? Handle that case too.
		if paramsMap, mapOk := req.Params.(map[string]interface{}); mapOk {
			// Attempt to marshal the map back to JSON bytes
			var marshalErr error
			rawParams, marshalErr = json.Marshal(paramsMap)
			if marshalErr != nil {
				log.Printf("Error marshalling params map for %s: %v", connID, marshalErr)
				return createJSONRPCError(req.ID, -32602, "Invalid parameters format (map marshal failed)", marshalErr.Error())
			}
			log.Printf("Handling 'tools/call' (JSON-RPC) for %s, Params: %s (from map)", connID, string(rawParams))
		} else {
			log.Printf("Invalid parameters format for tools/call (not json.RawMessage or map[string]interface{}): %T", req.Params)
			return createJSONRPCError(req.ID, -32602, "Invalid parameters format (expected JSON object)", nil)
		}
	} else {
		log.Printf("Handling 'tools/call' (JSON-RPC) for %s, Params: %s (from RawMessage)", connID, string(rawParams))
	}

	// Now, unmarshal the rawParams ([]byte) into ToolCallParams
	var params ToolCallParams
	if err := json.Unmarshal(rawParams, &params); err != nil {
		log.Printf("Error unmarshalling tools/call params for %s: %v", connID, err)
		return createJSONRPCError(req.ID, -32602, "Invalid parameters structure (unmarshal)", err.Error())
	}

	log.Printf("Executing tool '%s' for %s with input: %+v", params.ToolName, connID, params.Input)

	// --- Execute the actual tool call ---
	var resultPayload ToolResultPayload
	if reg.IsManagementTool(params.ToolName) {
		res := reg.runManagementTool(connID, params.ToolName, params.Input)
		text := res.text
		if !res.ok {
			text = fmt.Sprintf("Failed to execute tool '%s': %s", params.ToolName, text)
		}
		resultPayload = toolResultPayload(params.ToolName, req.ID, res.ok, text)
	} else {
		httpResp, execErr := executeRegisteredTool(reg, connID, &params)
		resultPayload = toolResultFromHTTP(params.ToolName, req.ID, httpResp, execErr)
	}

	// --- Send Response ---
	return jsonRPCResponse{
		Jsonrpc: "2.0",
		ID:      req.ID,        // Match request ID
		Result:  resultPayload, // Use the actual result payload
	}
}

// executeRegisteredTool resolves a fully qualified tool name to its API entry,
// selects the target (the API's active target, or the explicit 'target'
// argument) and executes the HTTP request against that target. When the target
// authenticates via a login endpoint, a session token is obtained first and
// attached to the request.
func executeRegisteredTool(reg *Registry, connID string, params *ToolCallParams) (*http.Response, error) {
	req, _, cfg, err := buildRegisteredRequestFor(reg, connID, params)
	if err != nil {
		return nil, err
	}
	return httpClientForConfig(cfg).Do(req)
}

// buildRegisteredRequest resolves a fully qualified tool name against the
// registry, applies target + auth, and returns the constructed *http.Request
// (without sending it) plus the resolved target name. Used both for execution
// and for dry-run/preview.
func buildRegisteredRequest(reg *Registry, params *ToolCallParams) (*http.Request, string, error) {
	req, name, _, err := buildRegisteredRequestFor(reg, "", params)
	return req, name, err
}

func buildRegisteredRequestFor(reg *Registry, connID string, params *ToolCallParams) (*http.Request, string, *config.Config, error) {
	api, tool, ok := reg.ResolveTool(params.ToolName)
	if !ok {
		return nil, "", nil, fmt.Errorf("operation details for tool '%s' not found", params.ToolName)
	}
	cleanArgs, target, targetCfg, err := reg.prepareCallArgsFor(connID, api, params.Input)
	if err != nil {
		return nil, "", nil, err
	}

	// Apply the API's authentication scheme using this target's credentials
	// (static API key, HTTP basic, or login/oauth2-derived session token).
	// Skip auth entirely when the called tool IS the API's login operation --
	// otherwise we'd try to authenticate to call the login endpoint itself.
	isLoginOp := tool.Name != "" && tool.Name == loginOperationFor(api)
	if !isLoginOp {
		if err := reg.applyAuthToConfig(api, target, targetCfg); err != nil {
			return nil, "", nil, err
		}
	}

	// Build against the bare tool name so the owning ToolSet's Operations map
	// resolves correctly; the synthetic 'target' argument was stripped above.
	local := *params
	local.ToolName = tool.Name
	local.Input = cleanArgs
	req, err := buildToolRequest(&local, api.ToolSet, targetCfg)
	if err != nil {
		return nil, "", nil, err
	}
	return req, target.Name, targetCfg, nil
}

// httpClientForConfig returns an *http.Client configured for the target (TLS
// verification disabled when the target sets insecure_skip_verify).
func httpClientForConfig(cfg *config.Config) *http.Client {
	c := &http.Client{Timeout: 30 * time.Second}
	if cfg != nil && cfg.InsecureSkipVerify {
		tr := &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // #nosec G402 -- explicit per-target opt-in for self-signed HTTPS
		}
		c.Transport = tr
	}
	return c
}

// toolResultFromHTTP converts an executed HTTP call into a ToolResultPayload.
func toolResultFromHTTP(toolName string, reqID interface{}, httpResp *http.Response, execErr error) ToolResultPayload {
	var resultPayload ToolResultPayload
	if execErr != nil {
		log.Printf("Error executing tool call '%s': %v", toolName, execErr)
		msg := fmt.Sprintf("Failed to execute tool '%s': %v", toolName, execErr)
		resultPayload = ToolResultPayload{
			Content:    []ToolResultContent{{Type: "text", Text: msg}},
			IsError:    true,
			Error:      &MCPError{Message: msg},
			ToolCallID: fmt.Sprintf("%v", reqID),
		}
		return resultPayload
	}

	defer httpResp.Body.Close() // Ensure body is closed
	bodyBytes, readErr := io.ReadAll(httpResp.Body)
	if readErr != nil {
		log.Printf("Error reading response body for tool '%s': %v", toolName, readErr)
		msg := fmt.Sprintf("Failed to read response from tool '%s': %v", toolName, readErr)
		return ToolResultPayload{
			Content:    []ToolResultContent{{Type: "text", Text: msg}},
			IsError:    true,
			Error:      &MCPError{Message: msg},
			ToolCallID: fmt.Sprintf("%v", reqID),
		}
	}

	log.Printf("Received response body for tool '%s': %s", toolName, string(bodyBytes))
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		// Error case: surface the API's error response body.
		return ToolResultPayload{
			Content:    []ToolResultContent{{Type: "text", Text: string(bodyBytes)}},
			IsError:    true,
			StatusCode: httpResp.StatusCode,
			Error: &MCPError{
				Code:    httpResp.StatusCode,
				Message: fmt.Sprintf("Tool '%s' API call failed with status %s", toolName, httpResp.Status),
			},
			ToolCallID: fmt.Sprintf("%v", reqID),
		}
	}
	return ToolResultPayload{
		Content:    []ToolResultContent{{Type: "text", Text: string(bodyBytes)}}, // TODO: Handle JSON responses properly if Content-Type indicates it
		StatusCode: httpResp.StatusCode,
		IsError:    false,
		ToolCallID: fmt.Sprintf("%v", reqID),
	}
}

// toolResultPayload builds a ToolResultPayload from an explicit text result
// (used by the registry management tools).
func toolResultPayload(toolName string, reqID interface{}, ok bool, text string) ToolResultPayload {
	payload := ToolResultPayload{
		Content:    []ToolResultContent{{Type: "text", Text: text}},
		ToolCallID: fmt.Sprintf("%v", reqID),
	}
	if !ok {
		payload.IsError = true
		payload.Error = &MCPError{Message: text}
	}
	return payload
}

// --- Helper Functions (Updated for JSON-RPC) ---

// sendJSONRPCResponse sends a JSON-RPC response *synchronously*.
// Keep this for now for sending synchronous errors on POST decode/read failures.
func sendJSONRPCResponse(w http.ResponseWriter, resp jsonRPCResponse) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("Error encoding JSON-RPC response (ID: %v) for ConnID %v: %v", resp.ID, resp.Error, err)
		// Attempt to send a plain text error if JSON encoding fails
		tryWriteHTTPError(w, http.StatusInternalServerError, "Internal Server Error encoding JSON-RPC response")
	}
	log.Printf("Sent JSON-RPC response: Method=%s, ID=%v", getMethodFromResponse(resp), resp.ID)
}

// createJSONRPCError creates a JSON-RPC error response.
func createJSONRPCError(id interface{}, code int, message string, data interface{}) jsonRPCResponse {
	jsonErr := &jsonError{Code: code, Message: message, Data: data}
	return jsonRPCResponse{
		Jsonrpc: "2.0",
		ID:      id, // Error response should echo the request ID
		Error:   jsonErr,
	}
}

// sendJSONRPCError sends a JSON-RPC error response.
func sendJSONRPCError(w http.ResponseWriter, connID string, id interface{}, code int, message string, data interface{}) {
	resp := createJSONRPCError(id, code, message, data)
	log.Printf("Sending JSON-RPC Error for ConnID %s, ID %v: Code=%d, Message='%s'", connID, id, code, message)
	sendJSONRPCResponse(w, resp)
}

// Helper to get the method name for logging purposes (from the result/error structure if possible)
func getMethodFromResponse(resp jsonRPCResponse) string {
	if resp.Result != nil {
		// Attempt to infer method from result structure if it has a type field
		if resMap, ok := resp.Result.(map[string]interface{}); ok {
			if methodType, typeOk := resMap["type"].(string); typeOk {
				return methodType + "_result"
			}
		}
		// Infer based on known result types if possible
		if _, ok := resp.Result.(map[string]interface{}); ok && resp.Result.(map[string]interface{})["tools"] != nil {
			return "tool_set"
		}
		// If not easily identifiable, just indicate success
		return "success"
	} else if resp.Error != nil {
		return "error"
	}
	return "unknown"
}

// tryWriteHTTPError attempts to write an HTTP error, ignoring failures.
func tryWriteHTTPError(w http.ResponseWriter, code int, message string) {
	if _, err := w.Write([]byte(message)); err != nil {
		log.Printf("Error writing plain HTTP error response: %v", err)
	}
	log.Printf("Sent plain HTTP error: %s (Code: %d)", message, code)
}

// broadcastToolsListChanged pushes a notifications/tools/list_changed message to
// every connected SSE client so they re-issue tools/list after a registry
// mutation. Delivery is best-effort: if a client's channel is full the
// notification is dropped (the client can still re-fetch on its next request).
func broadcastToolsListChanged() {
	notification := jsonRPCResponse{
		Jsonrpc: "2.0",
		Method:  "notifications/tools/list_changed",
	}
	connMutex.RLock()
	defer connMutex.RUnlock()
	for connID, ch := range activeConnections {
		if !initializedConnections[connID] {
			continue // client has not completed 'initialize'; a list_changed is meaningless
		}
		select {
		case ch <- notification:
			log.Printf("Sent notifications/tools/list_changed to %s", connID)
		default:
			log.Printf("Warning: dropped notifications/tools/list_changed for %s (channel full)", connID)
		}
	}
}

// markConnectionInitialized records that a session completed the initialize
// handshake, making it eligible for tools/list_changed notifications.
func markConnectionInitialized(connID string) {
	if connID == "" {
		return
	}
	connMutex.Lock()
	defer connMutex.Unlock()
	if _, active := activeConnections[connID]; active {
		initializedConnections[connID] = true
	}
}
