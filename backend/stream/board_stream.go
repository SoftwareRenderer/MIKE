package stream

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"kanban/backend/store"

	"github.com/coder/websocket"
)

// BoardStreamManager manages per-project WebSocket connections for board updates.
type BoardStreamManager struct {
	mu       sync.RWMutex
	clients  map[string]map[*websocket.Conn]bool // projectID -> set of connections
	store    *store.Store
}

// NewBoardStreamManager creates a new BoardStreamManager.
func NewBoardStreamManager(store *store.Store) *BoardStreamManager {
	return &BoardStreamManager{
		clients: make(map[string]map[*websocket.Conn]bool),
		store:   store,
	}
}

// getProjectClients returns the set of WebSocket connections for a project.
func (bsm *BoardStreamManager) getProjectClients(projectID string) map[*websocket.Conn]bool {
	bsm.mu.RLock()
	defer bsm.mu.RUnlock()
	return bsm.clients[projectID]
}

// add registers a new client for a project.
func (bsm *BoardStreamManager) add(projectID string, conn *websocket.Conn) {
	bsm.mu.Lock()
	defer bsm.mu.Unlock()
	if bsm.clients[projectID] == nil {
		bsm.clients[projectID] = make(map[*websocket.Conn]bool)
	}
	bsm.clients[projectID][conn] = true
}

// remove unregisters a client from a project.
func (bsm *BoardStreamManager) remove(projectID string, conn *websocket.Conn) {
	bsm.mu.Lock()
	defer bsm.mu.Unlock()
	if clients, ok := bsm.clients[projectID]; ok {
		delete(clients, conn)
		if len(clients) == 0 {
			delete(bsm.clients, projectID)
		}
	}
}

// Broadcast sends a board_refresh event to all clients of a project,
// and a project_counts event to ALL connected clients across all projects.
func (bsm *BoardStreamManager) Broadcast(projectID string) {
	bsm.mu.RLock()
	clients := bsm.clients[projectID]
	bsm.mu.RUnlock()

	msg, _ := json.Marshal(map[string]string{"type": "board_refresh"})
	for conn := range clients {
		if err := conn.Write(context.Background(), websocket.MessageText, msg); err != nil {
			// Client disconnected, will be cleaned up on read loop exit
		}
	}

	bsm.BroadcastProjectCounts()
}

// BroadcastAll sends a raw message to ALL connected WebSocket clients across all projects.
func (bsm *BoardStreamManager) BroadcastAll(msg []byte) {
	bsm.mu.RLock()
	defer bsm.mu.RUnlock()

	for _, conns := range bsm.clients {
		for conn := range conns {
			if err := conn.Write(context.Background(), websocket.MessageText, msg); err != nil {
				// Client disconnected
			}
		}
	}
}

// BroadcastProjectCounts computes and broadcasts task counts for all projects.
func (bsm *BoardStreamManager) BroadcastProjectCounts() {
	if bsm.store == nil {
		return
	}
	counts := bsm.store.GetAllProjectCounts()
	msg, _ := json.Marshal(map[string]interface{}{
		"type": "project_counts",
		"data": counts,
	})
	bsm.BroadcastAll(msg)
}

// HandleStream upgrades a WebSocket connection and subscribes to board updates.
func (bsm *BoardStreamManager) HandleStream(w http.ResponseWriter, r *http.Request, projectID string) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		log.Printf("board stream accept error: %v", err)
		return
	}

	bsm.add(projectID, conn)
	defer func() {
		bsm.remove(projectID, conn)
		conn.Close(websocket.StatusNormalClosure, "closed")
	}()

	for {
		_, _, err := conn.Read(context.Background())
		if err != nil {
			break
		}
	}
}
