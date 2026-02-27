package server

// wsMessage is the envelope for all server-to-browser WebSocket messages.
// Unexported because it is only used within the server package.
type wsMessage struct {
	Event string `json:"event"`
	Data  any    `json:"data"`
}
