package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024 * 1024, // 1MB buffer to accommodate larger JPEG screen frames
	WriteBufferSize: 1024 * 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// Hub manages all connected Admin clients and active Host screen feeds
type Hub struct {
	admins   map[*websocket.Conn]bool
	adminsMu sync.Mutex

	// Map of active host connections
	hosts   map[string]*websocket.Conn
	hostsMu sync.RWMutex
}

func newHub() *Hub {
	return &Hub{
		admins: make(map[*websocket.Conn]bool),
		hosts:  make(map[string]*websocket.Conn),
	}
}

func (h *Hub) AddHost(id string, conn *websocket.Conn) {
	h.hostsMu.Lock()
	h.hosts[id] = conn
	h.hostsMu.Unlock()
	log.Printf("Host connected: %s (remote: %s). Total hosts: %d\n", id, conn.RemoteAddr(), len(h.hosts))
	h.BroadcastHostList()
}

func (h *Hub) RemoveHost(id string) {
	h.hostsMu.Lock()
	delete(h.hosts, id)
	h.hostsMu.Unlock()
	log.Printf("Host disconnected: %s. Total hosts: %d\n", id, len(h.hosts))
	h.BroadcastHostList()
}

// SendToHost forwards a message from an admin to a specific host agent
func (h *Hub) SendToHost(id string, msgType int, data []byte) error {
	h.hostsMu.RLock()
	conn, exists := h.hosts[id]
	h.hostsMu.RUnlock()

	if !exists {
		return fmt.Errorf("host %s not connected", id)
	}
	return conn.WriteMessage(msgType, data)
}

func (h *Hub) AddAdmin(conn *websocket.Conn) {
	h.adminsMu.Lock()
	h.admins[conn] = true
	h.adminsMu.Unlock()
	log.Printf("Admin connected: %s. Total admins: %d\n", conn.RemoteAddr(), len(h.admins))
}

func (h *Hub) RemoveAdmin(conn *websocket.Conn) {
	h.adminsMu.Lock()
	delete(h.admins, conn)
	h.adminsMu.Unlock()
	log.Printf("Admin disconnected: %s. Total admins: %d\n", conn.RemoteAddr(), len(h.admins))
}

func (h *Hub) Broadcast(msgType int, data []byte) {
	h.adminsMu.Lock()
	defer h.adminsMu.Unlock()
	for admin := range h.admins {
		err := admin.WriteMessage(msgType, data)
		if err != nil {
			log.Println("Error broadcasting to admin:", err)
			admin.Close()
			delete(h.admins, admin)
		}
	}
}

// SendHostList sends the current online hosts list to a specific connection
func (h *Hub) SendHostList(conn *websocket.Conn) {
	h.hostsMu.RLock()
	hostsList := make([]string, 0, len(h.hosts))
	for id := range h.hosts {
		hostsList = append(hostsList, id)
	}
	h.hostsMu.RUnlock()

	msg := map[string]interface{}{
		"type":  "host_list",
		"hosts": hostsList,
	}
	data, _ := json.Marshal(msg)
	conn.WriteMessage(websocket.TextMessage, data)
}

// BroadcastHostList broadcasts the current list of online hosts to all admin clients
func (h *Hub) BroadcastHostList() {
	h.hostsMu.RLock()
	hostsList := make([]string, 0, len(h.hosts))
	for id := range h.hosts {
		hostsList = append(hostsList, id)
	}
	h.hostsMu.RUnlock()

	msg := map[string]interface{}{
		"type":  "host_list",
		"hosts": hostsList,
	}
	data, _ := json.Marshal(msg)
	h.Broadcast(websocket.TextMessage, data)
}

var hub = newHub()

func serveHTML(w http.ResponseWriter, r *http.Request, filename string) {
	// Try local path first (if run from inside the drs directory)
	if _, err := os.Stat(filename); err == nil {
		http.ServeFile(w, r, filename)
		return
	}
	// Try prefixed path (if run from root directory)
	prefixed := filepath.Join("drs", filename)
	if _, err := os.Stat(prefixed); err == nil {
		http.ServeFile(w, r, prefixed)
		return
	}
	http.Error(w, fmt.Sprintf("File not found: %s", filename), http.StatusNotFound)
}

func homepage(w http.ResponseWriter, r *http.Request) {
	serveHTML(w, r, "index.html")
}

func adminPage(w http.ResponseWriter, r *http.Request) {
	serveHTML(w, r, "admin.html")
}

// wsEndpoint handles both incoming Host streams and Admin viewers
func wsEndpoint(w http.ResponseWriter, r *http.Request) {
	role := r.URL.Query().Get("role")
	if role != "host" && role != "admin" {
		http.Error(w, "Invalid role query parameter. Must be 'host' or 'admin'.", http.StatusBadRequest)
		return
	}

	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("Upgrade error:", err)
		return
	}

	if role == "host" {
		id := r.URL.Query().Get("id")
		if id == "" {
			id = fmt.Sprintf("host_%s", ws.RemoteAddr().String())
		}

		hub.AddHost(id, ws)
		defer func() {
			hub.RemoveHost(id)
			ws.Close()
		}()

		// Read messages from host:
		// - Binary = JPEG frame → Wrap and broadcast
		// - Text   = screen_info → Inject host ID and broadcast
		for {
			messageType, payload, err := ws.ReadMessage()
			if err != nil {
				log.Printf("Host %s read error: %v\n", id, err)
				break
			}
			if messageType == websocket.BinaryMessage {
				// Wrap format: [1 byte ID len] [N bytes ID string] [M bytes JPEG]
				idBytes := []byte(id)
				idLen := byte(len(idBytes))
				wrapped := make([]byte, 1+len(idBytes)+len(payload))
				wrapped[0] = idLen
				copy(wrapped[1:], idBytes)
				copy(wrapped[1+len(idBytes):], payload)

				hub.Broadcast(websocket.BinaryMessage, wrapped)
			} else if messageType == websocket.TextMessage {
				// Augment host messages (screen_info) with its host ID
				var msg map[string]interface{}
				if err := json.Unmarshal(payload, &msg); err == nil {
					msg["host_id"] = id
					updatedPayload, _ := json.Marshal(msg)
					hub.Broadcast(websocket.TextMessage, updatedPayload)
				}
			}
		}
	} else if role == "admin" {
		hub.AddAdmin(ws)
		defer func() {
			hub.RemoveAdmin(ws)
			ws.Close()
		}()

		// Immediately send active host list to new admin
		hub.SendHostList(ws)

		// Read messages from admin:
		// - Text = JSON commands (mouse_move, mouse_click, key_press, mouse_scroll)
		//   → Extract host_id and forward to targeted host
		for {
			messageType, payload, err := ws.ReadMessage()
			if err != nil {
				break
			}
			if messageType == websocket.TextMessage {
				var cmd struct {
					HostID string `json:"host_id"`
				}
				if err := json.Unmarshal(payload, &cmd); err == nil && cmd.HostID != "" {
					err = hub.SendToHost(cmd.HostID, websocket.TextMessage, payload)
					if err != nil {
						log.Printf("Failed to forward admin command to host %s: %v\n", cmd.HostID, err)
					}
				}
			}
		}
	}
}

func setupRoutes() {
	http.HandleFunc("/", homepage)
	http.HandleFunc("/admin", adminPage)
	http.HandleFunc("/ws", wsEndpoint)
}

func main() {
	fmt.Println("Go WebSocket Screen Sharing Server running on :8080")
	setupRoutes()
	log.Fatal(http.ListenAndServe(":8080", nil))
}
