package managers

import (
	"encoding/json"
	"net/http"
	"time"

	"kanban/backend/config"
	"kanban/backend/httputil"
	"kanban/backend/store"
	"kanban/backend/types"

	"github.com/coder/websocket"
	"github.com/google/uuid"
)

// HomeAgentHandler handles home agent operations.
type HomeAgentHandler struct {
	store       *store.Store
	configStore *config.ConfigStore
}

// NewHomeAgentHandler creates a new HomeAgentHandler.
func NewHomeAgentHandler(store *store.Store, configStore *config.ConfigStore) *HomeAgentHandler {
	return &HomeAgentHandler{store: store, configStore: configStore}
}

// HandleMessage sends a message to the home agent.
func (h *HomeAgentHandler) HandleMessage(w http.ResponseWriter, r *http.Request, projectID string) {
	var req types.CreateHomeAgentMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.JSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	msg := &types.HomeAgentMessage{
		ID:        uuid.New().String(),
		ProjectID: projectID,
		Role:      "user",
		Content:   req.Content,
		CreatedAt: time.Now().UTC(),
	}
	if err := h.store.CreateHomeAgentMessage(msg); err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httputil.JSONResponse(w, http.StatusOK, msg)
}

// HandleMessages gets home agent conversation.
func (h *HomeAgentHandler) HandleMessages(w http.ResponseWriter, r *http.Request, projectID string) {
	msgs, err := h.store.GetHomeAgentMessages(projectID)
	if err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httputil.JSONResponse(w, http.StatusOK, msgs)
}

// HandleStream handles WebSocket stream for home agent.
func (h *HomeAgentHandler) HandleStream(w http.ResponseWriter, r *http.Request, projectID string) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer conn.CloseNow()

	welcome := types.HomeAgentMessage{Content: "Home agent connected. How can I help?", Role: "assistant"}
	data, _ := json.Marshal(map[string]interface{}{"type": "assistant_token", "data": welcome})
	conn.Write(r.Context(), websocket.MessageText, data)

	for {
		_, msg, err := conn.Read(r.Context())
		if err != nil {
			break
		}
		var input struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(msg, &input); err != nil {
			continue
		}
		if input.Type == "user_message" {
			var content struct {
				Content string `json:"content"`
			}
			json.Unmarshal(input.Data, &content)
			userMsg := &types.HomeAgentMessage{ID: uuid.New().String(), ProjectID: projectID, Role: "user", Content: content.Content, CreatedAt: time.Now().UTC()}
			h.store.CreateHomeAgentMessage(userMsg)
			response := types.HomeAgentMessage{Content: "I received: " + content.Content, Role: "assistant"}
			respData, _ := json.Marshal(map[string]interface{}{"type": "assistant_token", "data": response})
			conn.Write(r.Context(), websocket.MessageText, respData)
		}
	}
}
