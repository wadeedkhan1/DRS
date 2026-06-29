package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"image/jpeg"
	"log"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/go-vgo/robotgo"
	"github.com/gorilla/websocket"
	"github.com/kbinani/screenshot"
)

const Ticker = 40
const JPEGQuality = 40

// Command represents a remote control command from the admin
type Command struct {
	Type      string `json:"type"`
	X         int    `json:"x"`
	Y         int    `json:"y"`
	Button    string `json:"button"`
	Key       string `json:"key"`
	Direction string `json:"direction"`
	Amount    int    `json:"amount"`
}

// keyMap translates browser key names to robotgo key names
var keyMap = map[string]string{
	"Enter":       "enter",
	"Backspace":   "backspace",
	"Tab":         "tab",
	"Escape":      "escape",
	" ":           "space",
	"ArrowUp":     "up",
	"ArrowDown":   "down",
	"ArrowLeft":   "left",
	"ArrowRight":  "right",
	"Delete":      "delete",
	"Home":        "home",
	"End":         "end",
	"PageUp":      "pageup",
	"PageDown":    "pagedown",
	"Insert":      "insert",
	"F1":          "f1",
	"F2":          "f2",
	"F3":          "f3",
	"F4":          "f4",
	"F5":          "f5",
	"F6":          "f6",
	"F7":          "f7",
	"F8":          "f8",
	"F9":          "f9",
	"F10":         "f10",
	"F11":         "f11",
	"F12":         "f12",
	"Control":     "",
	"Shift":       "",
	"Alt":         "",
	"Meta":        "",
	"CapsLock":    "capslock",
	"NumLock":     "numlock",
	"ScrollLock":  "scrolllock",
	"PrintScreen": "printscreen",
	"Pause":       "pause",
}

func handleCommand(data []byte) {
	var cmd Command
	if err := json.Unmarshal(data, &cmd); err != nil {
		log.Printf("Invalid command JSON: %v\n", err)
		return
	}

	switch cmd.Type {
	case "mouse_move":
		robotgo.Move(cmd.X, cmd.Y)

	case "mouse_click":
		button := cmd.Button
		if button == "" {
			button = "left"
		}
		robotgo.Click(button, false)

	case "mouse_scroll":
		amount := cmd.Amount
		if amount <= 0 {
			amount = 3
		}
		if cmd.Direction == "up" {
			robotgo.Scroll(0, amount) // scroll up
		} else {
			robotgo.Scroll(0, -amount) // scroll down
		}

	case "key_press":
		key := cmd.Key
		if key == "" {
			return
		}

		// Skip modifier-only keys
		if mapped, ok := keyMap[key]; ok {
			if mapped == "" {
				return // modifier key, skip
			}
			key = mapped
		} else {
			// Single character keys — robotgo expects lowercase
			key = strings.ToLower(key)
		}

		robotgo.KeyTap(key)

	default:
		log.Printf("Unknown command type: %s\n", cmd.Type)
	}
}

func main() {
	serverAddr := flag.String("server", "192.168.1.13:8080", "Server address (e.g. localhost:8080 or your-app.onrender.com)")
	useSSL := flag.Bool("ssl", false, "Use secure WebSocket connection (wss)")
	flag.Parse()

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)

	hostname, err := os.Hostname()
	if err != nil {
		hostname = "host"
	}
	clientID := fmt.Sprintf("%s_%d", hostname, os.Getpid())
	log.Printf("Starting host client agent with ID: %s", clientID)

	scheme := "ws"
	if *useSSL {
		scheme = "wss"
	}

	u := url.URL{
		Scheme:   scheme,
		Host:     *serverAddr,
		Path:     "/ws",
		RawQuery: fmt.Sprintf("role=host&id=%s", url.QueryEscape(clientID)),
	}
	log.Printf("Connecting to screen-sharing server at %s...", u.String())

	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		log.Fatalf("Connection failed to %s. Error: %v", u.String(), err)
	}
	defer conn.Close()
	log.Printf("Connection established successfully for ID: %s! Starting stream...", clientID)

	n := screenshot.NumActiveDisplays()
	if n <= 0 {
		log.Fatal("Error: No active displays detected on the host system.")
	}
	bounds := screenshot.GetDisplayBounds(0)
	log.Printf("Primary Screen bounds: %dx%d starting at (%d,%d)\n", bounds.Dx(), bounds.Dy(), bounds.Min.X, bounds.Min.Y)

	// Send screen dimensions to server so admin can scale mouse coordinates
	screenInfo := map[string]interface{}{
		"type":   "screen_info",
		"width":  bounds.Dx(),
		"height": bounds.Dy(),
	}
	infoJSON, _ := json.Marshal(screenInfo)
	if err := conn.WriteMessage(websocket.TextMessage, infoJSON); err != nil {
		log.Printf("Warning: failed to send screen_info: %v\n", err)
	}
	log.Printf("Sent screen_info: %s\n", string(infoJSON))

	ticker := time.NewTicker(Ticker * time.Millisecond)
	defer ticker.Stop()

	done := make(chan struct{})

	// Read incoming messages — text = admin commands, close = disconnect
	go func() {
		defer close(done)
		for {
			messageType, payload, err := conn.ReadMessage()
			if err != nil {
				log.Println("Server connection terminated:", err)
				return
			}
			if messageType == websocket.TextMessage {
				handleCommand(payload)
			}
		}
	}()

	var frameCount int
	var byteCount int
	statsTicker := time.NewTicker(5 * time.Second)
	defer statsTicker.Stop()

	for {
		select {
		case <-done:
			log.Println("WebSocket connection closed. Stopping stream.")
			return

		case <-ticker.C:
			img, err := screenshot.CaptureRect(bounds)
			if err != nil {
				log.Println("Capture error:", err)
				continue
			}

			var buf bytes.Buffer
			err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: JPEGQuality})
			if err != nil {
				log.Println("JPEG compression error:", err)
				continue
			}

			err = conn.WriteMessage(websocket.BinaryMessage, buf.Bytes())
			if err != nil {
				log.Println("Send frame error:", err)
				return
			}

			frameCount++
			byteCount += buf.Len()

		case <-statsTicker.C:
			if frameCount > 0 {
				avgSize := byteCount / frameCount / 1024
				log.Printf("[Stream Stats] Sent %d frames in last 5s. Avg size: %d KB. Rate: %.1f FPS\n",
					frameCount, avgSize, float64(frameCount)/5.0)
				frameCount = 0
				byteCount = 0
			}

		case <-interrupt:
			log.Println("Interrupt received. Stopping stream and cleaning up...")
			err := conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			if err != nil {
				log.Println("Error writing close frame:", err)
			}
			select {
			case <-done:
			case <-time.After(1 * time.Second):
			}
			return
		}
	}
}
