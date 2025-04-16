package main

import (
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/alexedwards/scs/v2"
	_ "github.com/go-sql-driver/mysql" // MySQL driver
	"github.com/ivahaev/amigo"
	"github.com/joho/godotenv"
)

//go:embed templates/* static/*
var content embed.FS

// Initialize session manager
var sessionManager *scs.SessionManager

// Global cache for extension states
type ExtensionCache struct {
	mu     sync.RWMutex
	states map[string]*Endpoint
}

var extensionCache = &ExtensionCache{
	states: make(map[string]*Endpoint),
}

var globalBroadcaster *AMIBroadcaster

func DeviceStateChangeHandler(m map[string]string) {
	// Only handle device state events for SIP/PJSIP devices
	if device := m["Device"]; strings.HasPrefix(device, "PJSIP/") || strings.HasPrefix(device, "SIP/") {
		// Only process state-related events
		if event := m["Event"]; event == "DeviceStateChange" || event == "DeviceState" {
			ext := strings.TrimPrefix(strings.TrimPrefix(device, "PJSIP/"), "SIP/")
			state := m["State"]
			readableState := getHumanReadableState(state)
			// Only process numeric extensions
			if _, err := strconv.Atoi(ext); err == nil {
				log.Printf("State change: %s -> %s", ext, readableState) // Keep this as regular log for important state changes
				extensionCache.mu.Lock()
				if endpoint, exists := extensionCache.states[ext]; exists {
					slog.Debug("Existing endpoint state change", "extension", ext, "old_state", endpoint.Status, "new_state", readableState)
					endpoint.Status = readableState
				} else {
					slog.Debug("New endpoint added", "extension", ext, "state", readableState)
					extensionCache.states[ext] = &Endpoint{
						Extension:   ext,
						Description: "", // Empty description for new endpoints
						Status:      readableState,
					}
				}
				extensionCache.mu.Unlock()

				// Broadcast the state change to connected clients with filtering based on extension length
				if globalBroadcaster != nil {
					slog.Debug("Broadcasting filtered event", "extension", ext, "state", readableState)
					slog.Debug("Connected clients", "count", globalBroadcaster.ClientCount())
					globalBroadcaster.BroadcastFilteredEvent(ext, readableState)
				}
			}
		}
	}
}

func DefaultHandler(m map[string]string) {
	event := m["Event"]
	// Skip common events and CDRPROSYNC user events
	if event != "ChallengeSent" &&
		event != "SuccessfulAuth" &&
		event != "RequestBadFormat" &&
		event != "ChallengeResponseFailed" &&
		!strings.HasPrefix(event, "RTCP") &&
		!(event == "UserEvent" && m["UserEvent"] == "CDRPROSYNC") {
		fmt.Printf("Default handler: %+v\n", m)
	}
}

// AMIBroadcaster handles broadcasting AMI events to clients
type AMIBroadcaster struct {
	clients map[chan string]*ClientInfo
	mu      sync.RWMutex
}

// ClientInfo stores information about a connected client
type ClientInfo struct {
	Authenticated bool
}

// NewAMIBroadcaster creates a new AMI broadcaster
func NewAMIBroadcaster() *AMIBroadcaster {
	return &AMIBroadcaster{
		clients: make(map[chan string]*ClientInfo),
	}
}

// Subscribe registers a new client channel for receiving events
func (b *AMIBroadcaster) Subscribe(authenticated bool) (chan string, func()) {
	// Increase buffer size to handle bursts of events better
	events := make(chan string, 100)

	b.mu.Lock()
	b.clients[events] = &ClientInfo{
		Authenticated: authenticated,
	}
	b.mu.Unlock()

	// Return the channel and an unsubscribe function
	return events, func() {
		b.mu.Lock()
		delete(b.clients, events)
		close(events)
		b.mu.Unlock()
	}
}

// ClientCount returns the number of connected clients
func (b *AMIBroadcaster) ClientCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.clients)
}

// BroadcastEvent sends an event to all connected clients
func (b *AMIBroadcaster) BroadcastEvent(event string) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	activeClients := 0
	skippedClients := 0

	for client := range b.clients {
		// Use a timeout for sending to prevent complete blocking
		select {
		case client <- event:
			activeClients++
		case <-time.After(100 * time.Millisecond):
			// If we can't send within 100ms, log it and skip
			skippedClients++
			log.Printf("Warning: Client buffer full, message skipped")
		}
	}

	if skippedClients > 0 {
		log.Printf("Warning: Broadcast partially complete: %d active clients, %d skipped", activeClients, skippedClients)
	} else {
		slog.Debug("Broadcast complete", "active_clients", activeClients)
	}

	// Debug: print the event that was broadcast
	slog.Debug("Broadcast event content", "event", event)
}

// BroadcastFilteredEvent sends an event to clients based on authentication status and extension length
func (b *AMIBroadcaster) BroadcastFilteredEvent(ext string, state string) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	activeClients := 0
	skippedClients := 0
	extLen := len(ext)

	for client, info := range b.clients {
		// Only send short extensions (4 or fewer digits) to authenticated clients
		if extLen <= 4 && !info.Authenticated {
			continue // Skip this client
		}

		// Format the event message
		eventMsg := fmt.Sprintf("data: %s %s\n\n", ext, state)

		// Use a timeout for sending to prevent complete blocking
		select {
		case client <- eventMsg:
			activeClients++
		case <-time.After(100 * time.Millisecond):
			// If we can't send within 100ms, log it and skip
			skippedClients++
			log.Printf("Warning: Client buffer full, message skipped")
		}
	}

	if skippedClients > 0 {
		log.Printf("Warning: Filtered broadcast partially complete: %d active clients, %d skipped", activeClients, skippedClients)
	} else {
		slog.Debug("Filtered broadcast complete", "active_clients", activeClients)
	}

	// Debug: print the event that was broadcast
	slog.Debug("Broadcast filtered event", "extension", ext, "state", state)
}

// Endpoint represents a phone extension
type Endpoint struct {
	Extension   string
	Description string
	Status      string
	Disabled    bool
}

func getDeviceDescriptions() (map[string]string, error) {
	devices := make(map[string]string)

	// If DB_HOST is not defined, return empty descriptions
	if os.Getenv("DB_HOST") == "" {
		return devices, nil
	}

	// Connect to MySQL
	db, err := sql.Open("mysql", fmt.Sprintf("%s:%s@tcp(%s)/%s",
		os.Getenv("DB_USER"),
		os.Getenv("DB_PASS"),
		os.Getenv("DB_HOST"),
		os.Getenv("DB_NAME")))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %v", err)
	}
	defer db.Close()

	// Query device descriptions
	rows, err := db.Query("SELECT id, description FROM devices")
	if err != nil {
		return nil, fmt.Errorf("failed to query devices: %v", err)
	}
	defer rows.Close()

	// Process results
	for rows.Next() {
		var id, description string
		if err := rows.Scan(&id, &description); err != nil {
			return nil, fmt.Errorf("failed to scan row: %v", err)
		}
		// Only include numeric extensions
		if _, err := strconv.Atoi(id); err == nil {
			devices[id] = description
		}
	}

	return devices, nil
}

// Initialize extension cache with descriptions and default states
func initializeExtensionCache() error {
	descriptions, err := getDeviceDescriptions()
	if err != nil {
		return fmt.Errorf("failed to get device descriptions: %v", err)
	}

	extensionCache.mu.Lock()
	defer extensionCache.mu.Unlock()

	// Initialize cache with descriptions
	for ext, desc := range descriptions {
		extensionCache.states[ext] = &Endpoint{
			Extension:   ext,
			Description: desc,
			Status:      "Unavailable",
		}
	}

	return nil
}

// Helper function to convert device state to human readable format
func getHumanReadableState(state string) string {
	switch strings.ToUpper(state) {
	case "INUSE":
		return "In use"
	case "NOT_INUSE", "IDLE":
		return "Not in use"
	case "RINGING":
		return "Ringing"
	case "BUSY":
		return "Busy"
	case "UNAVAILABLE", "INVALID", "UNKNOWN", "":
		return "Unavailable"
	default:
		return "Unknown"
	}
}

func main() {
	// Load environment variables
	if err := godotenv.Load(); err != nil {
		log.Fatal("Error loading .env file")
	}
	
	// Configure slog based on DEBUG environment variable
	logLevel := slog.LevelInfo
	if os.Getenv("DEBUG") != "" {
		logLevel = slog.LevelDebug
	}
	
	// Create a text handler with the appropriate level
	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	})
	
	// Set the default logger
	slog.SetDefault(slog.New(handler))

	// Initialize extension cache
	if err := initializeExtensionCache(); err != nil {
		log.Printf("Warning: Failed to initialize extension cache: %v", err)
	}

	// Initialize session manager
	sessionManager = scs.New()
	sessionManager.Lifetime = 24 * time.Hour
	// Set secure cookie options
	sessionManager.Cookie.Secure = true
	sessionManager.Cookie.HttpOnly = true
	sessionManager.Cookie.SameSite = http.SameSiteStrictMode

	// Create AMI broadcaster
	globalBroadcaster = NewAMIBroadcaster()

	// Create AMI settings
	amiSettings := &amigo.Settings{
		Host:              os.Getenv("AMI_HOST"),
		Port:              os.Getenv("AMI_PORT"),
		Username:          os.Getenv("AMI_USER"),
		Password:          os.Getenv("AMI_PASS"),
		DialTimeout:       10 * time.Second,
		ReconnectInterval: 5 * time.Second,
	}

	// Create AMI client with settings
	ami := amigo.New(amiSettings)

	// Register event handlers first
	// Channel to signal AMI connection ready
	amiReady := make(chan bool)

	ami.On("connect", func(message string) {
		log.Printf("Connected: %s", message)
		amiReady <- true
	})
	ami.On("error", func(message string) {
		log.Printf("CONNECTION ERROR: %s", message)
	})
	ami.RegisterHandler("DeviceStateChange", DeviceStateChangeHandler)
	ami.RegisterDefaultHandler(DefaultHandler)

	// Connect to AMI
	ami.Connect()
	defer ami.Close()

	// Wait for AMI to be ready
	select {
	case <-amiReady:
		log.Printf("AMI connection ready")
	case <-time.After(5 * time.Second):
		log.Printf("WARNING: Timeout waiting for AMI connection")
	}

	// Initialize extension cache first
	if err := initializeExtensionCache(); err != nil {
		log.Printf("Error initializing extension cache: %v", err)
	} else {
		slog.Debug("Extension cache initialized with descriptions")
		extensionCache.mu.RLock()
		for ext, endpoint := range extensionCache.states {
			slog.Debug("Extension description", "extension", ext, "description", endpoint.Description)
		}
		extensionCache.mu.RUnlock()
	}

	// Create a channel to receive device state events
	eventChan := make(chan map[string]string, 100)
	ami.SetEventChannel(eventChan)

	// Request initial device states
	log.Printf("Requesting initial device states")
	resp, err := ami.Action(map[string]string{"Action": "DeviceStateList", "ActionID": "init"})
	log.Printf("DeviceStateList response: %+v", resp)
	if err != nil {
		log.Printf("Error getting device states: %v", err)
	} else {
		// Wait for events and process them
		timeout := time.After(10 * time.Second) // Increased timeout for initial state gathering
	deviceLoop:
		for {
			select {
			case event := <-eventChan:
				// Check if this is a DeviceState or DeviceStateChange event
				if event["Event"] == "DeviceState" || event["Event"] == "DeviceStateChange" {
					device := event["Device"]
					state := strings.ToUpper(event["State"])
					log.Printf("Got device state: %s = %s", device, state)
					if device != "" && (strings.HasPrefix(device, "PJSIP/") || strings.HasPrefix(device, "SIP/")) {
						ext := strings.TrimPrefix(strings.TrimPrefix(device, "PJSIP/"), "SIP/")
						// Skip non-numeric extensions
						_, err := strconv.Atoi(ext)
						if err != nil {
							continue
						}
						extensionCache.mu.Lock()
						if endpoint, exists := extensionCache.states[ext]; exists {
							readableState := getHumanReadableState(state)
							log.Printf("Updating existing endpoint %s: %s -> %s", ext, endpoint.Status, readableState)
							endpoint.Status = readableState
						} else {
							readableState := getHumanReadableState(state)
							log.Printf("Creating new endpoint %s with state %s", ext, readableState)
							extensionCache.states[ext] = &Endpoint{
								Extension: ext,
								Status:    getHumanReadableState(state),
							}
						}
						extensionCache.mu.Unlock()
					}
				} else if event["Event"] == "DeviceStateListComplete" {
					// All device states received
					log.Printf("Device state list complete")
					// Broadcast all current states to clients with filtering
					extensionCache.mu.RLock()
					for ext, endpoint := range extensionCache.states {
						log.Printf("Broadcasting initial state for extension %s with state %s", ext, endpoint.Status)
						globalBroadcaster.BroadcastFilteredEvent(ext, endpoint.Status)
					}
					extensionCache.mu.RUnlock()
					break deviceLoop
				}
			case <-timeout:
				// Timeout waiting for events
				log.Printf("Timeout waiting for device states")
				break deviceLoop
			}
		}
	}

	// Stop receiving events
	ami.SetEventChannel(nil)

	// Log current states in readable format
	extensionCache.mu.RLock()
	slog.Debug("Current device states")
	for ext, endpoint := range extensionCache.states {
		slog.Debug("Extension state", "extension", ext, "status", endpoint.Status, "description", endpoint.Description)
	}
	extensionCache.mu.RUnlock()

	// Set up HTTP routes
	mux := http.NewServeMux()

	// Login handler
	mux.HandleFunc("/api/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var loginData struct {
			Password string `json:"password"`
		}

		if err := json.NewDecoder(r.Body).Decode(&loginData); err != nil {
			http.Error(w, "Invalid request", http.StatusBadRequest)
			return
		}

		// Check password against environment variable
		if loginData.Password != os.Getenv("ADMIN_PASSWORD") {
			http.Error(w, "Invalid password", http.StatusUnauthorized)
			return
		}

		// Create session
		sessionManager.Put(r.Context(), "authenticated", true)

		w.WriteHeader(http.StatusOK)
	})

	// Serve static files
	mux.Handle("/static/", http.FileServer(http.FS(content)))

	// Serve robots.txt and favicon.ico directly from static directory
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		content, err := content.ReadFile("static/robots.txt")
		if err != nil {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Write(content)
	})

	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		content, err := content.ReadFile("static/favicon.ico")
		if err != nil {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "image/x-icon")
		w.Write(content)
	})

	// Handle main page
	tmpl := template.Must(template.ParseFS(content, "templates/index.html"))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}

		// Check authentication
		authenticated := sessionManager.GetBool(r.Context(), "authenticated")
		slog.Debug("Authentication status", "authenticated", authenticated)

		// Create endpoint list from cache
		endpoints := []Endpoint{}
		extensionCache.mu.RLock()
		for _, endpoint := range extensionCache.states {
			// Only show numeric extensions
			if _, err := strconv.Atoi(endpoint.Extension); err == nil {
				// Show all extensions if authenticated, otherwise only show extensions longer than 4 digits
				if authenticated || len(endpoint.Extension) > 4 {
					endpoints = append(endpoints, *endpoint)
				}
			}
		}
		extensionCache.mu.RUnlock()

		// Sort endpoints numerically by extension
		sort.Slice(endpoints, func(i, j int) bool {
			// Convert extensions to integers for comparison
			num1, err1 := strconv.Atoi(endpoints[i].Extension)
			num2, err2 := strconv.Atoi(endpoints[j].Extension)
			// If conversion fails, fall back to string comparison
			if err1 != nil || err2 != nil {
				return endpoints[i].Extension < endpoints[j].Extension
			}
			return num1 < num2
		})

		// Get UI customization from environment variables or use defaults
		pageTitle := os.Getenv("PAGE_TITLE")
		if pageTitle == "" {
			pageTitle = "SIP Status"
		}

		brandImage := os.Getenv("BRAND_IMAGE")
		if brandImage == "" {
			brandImage = "/static/img/dvnz-96x96.png"
		}

		brandAlt := os.Getenv("BRAND_ALT")
		if brandAlt == "" {
			brandAlt = "Digital Voice NZ Logo"
		}

		voipImage := os.Getenv("VOIP_IMAGE")
		if voipImage == "" {
			voipImage = "/static/img/voip.png"
		}

		voipAlt := os.Getenv("VOIP_ALT")
		if voipAlt == "" {
			voipAlt = "Stylized VoIP phone with HT as handset"
		}

		tmpl.Execute(w, map[string]interface{}{
			"Endpoints":  endpoints,
			"PageTitle":  pageTitle,
			"BrandImage": brandImage,
			"BrandAlt":   brandAlt,
			"VoipImage":  voipImage,
			"VoipAlt":    voipAlt,
		})
	})

	// Handle SSE endpoint
	// Test endpoint to trigger a state update
	mux.HandleFunc("/test-update", func(w http.ResponseWriter, r *http.Request) {
		ext := r.URL.Query().Get("ext")
		state := r.URL.Query().Get("state")
		if ext == "" {
			ext = "12345"
		}
		if state == "" {
			state = "In use"
		}

		log.Printf("Manual test update for extension %s to state %s", ext, state)

		// Broadcast to clients with filtering based on extension length
		if globalBroadcaster != nil {
			slog.Debug("Test broadcast", "extension", ext, "state", state)
			globalBroadcaster.BroadcastFilteredEvent(ext, state)
		}

		fmt.Fprintf(w, "Sent update for extension %s with state %s", ext, state)
	})

	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		// Get client info for logging - check common proxy headers
		clientIP := r.Header.Get("X-Forwarded-For")
		if clientIP == "" {
			clientIP = r.Header.Get("X-Real-IP")
		}
		if clientIP == "" {
			clientIP = r.Header.Get("CF-Connecting-IP") // Cloudflare
		}
		if clientIP == "" {
			clientIP = r.RemoteAddr
		}
		// If X-Forwarded-For contains multiple IPs, use the first one (original client)
		if idx := strings.Index(clientIP, ","); idx != -1 {
			clientIP = strings.TrimSpace(clientIP[:idx])
		}
		log.Printf("New SSE connection from %s", clientIP)

		// Set headers for SSE
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Access-Control-Allow-Origin", "*") // Allow cross-origin requests
		w.Header().Set("X-Accel-Buffering", "no")

		slog.Debug("SSE headers set", "client_ip", clientIP)

		// Check if user is authenticated
		isAuthenticated := sessionManager.GetBool(r.Context(), "authenticated")

		// Subscribe to AMI events using broadcaster
		events, unsubscribe := globalBroadcaster.Subscribe(isAuthenticated)
		defer unsubscribe()

		// Send current state of all extensions to the new client
		extensionCache.mu.RLock()
		slog.Debug("Sending initial states", "client_ip", clientIP)
		for ext, endpoint := range extensionCache.states {
			// Only send extensions under 5 digits if authenticated
			if endpoint.Status != "" && (isAuthenticated || len(ext) >= 5) {
				stateMsg := fmt.Sprintf("%s %s", ext, endpoint.Status)
				msg := fmt.Sprintf("data: %s\n\n", stateMsg)
				slog.Debug("Sending initial state", "client_ip", clientIP, "extension", ext, "status", endpoint.Status)
				fmt.Fprint(w, msg)
				w.(http.Flusher).Flush()
			}
		}
		extensionCache.mu.RUnlock()
		slog.Debug("Finished sending initial states", "client_ip", clientIP)

		// Send initial connection message
		initialMsg := "data: Connected to updates\n\n"
		fmt.Fprint(w, initialMsg)
		w.(http.Flusher).Flush()
		slog.Debug("Sent initial message", "client_ip", clientIP, "message", initialMsg)

		// Start keep-alive ticker
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		// Handle events and keep-alive
		for {
			select {
			case <-r.Context().Done():
				log.Printf("Client %s context done", clientIP)
				return
			case event := <-events:
				// Check if this is a direct broadcast message (already formatted as SSE)
				if strings.HasPrefix(event, "data: ") {
					slog.Debug("Forwarding direct SSE message", "client_ip", clientIP, "event", event)
					fmt.Fprint(w, event)
					w.(http.Flusher).Flush()
					slog.Debug("Sent direct update", "client_ip", clientIP)
				} else if strings.Contains(event, "DeviceStateChange") {
					parts := strings.Split(event, "\r\n")
					var device, state string
					for _, part := range parts {
						if strings.HasPrefix(part, "Device: ") {
							device = strings.TrimPrefix(part, "Device: ")
						} else if strings.HasPrefix(part, "State: ") {
							state = strings.TrimPrefix(part, "State: ")
						}
					}
					if device != "" && state != "" {
						// Convert AMI state to display state
						displayState := "Not in use"
						switch state {
						case "INUSE":
							displayState = "In use"
						case "RINGING":
							displayState = "Ringing"
						case "UNAVAILABLE":
							displayState = "Unavailable"
						case "INVALID":
							displayState = "Invalid"
						}
						// Extract extension from device path
						ext := strings.TrimPrefix(strings.TrimPrefix(device, "PJSIP/"), "SIP/")
						// Only send updates for extensions >= 5 digits if not authenticated
						if isAuthenticated || len(ext) >= 5 {
							msg := fmt.Sprintf("data: %s %s\n\n", ext, displayState)
							slog.Debug("Sending SSE event", "client_ip", clientIP, "message", msg)
							fmt.Fprint(w, msg)
							w.(http.Flusher).Flush()
							slog.Debug("Sent update", "client_ip", clientIP, "extension", ext, "state", displayState)
						}
					}
				}
			case <-ticker.C:
				// Send keep-alive message as a comment (just a colon)
				msg := ":\n\n"
				fmt.Fprint(w, msg)
				w.(http.Flusher).Flush()
				slog.Debug("Sent keep-alive", "client_ip", clientIP)
			}
		}
	})

	// Get server configuration
	serverIP := os.Getenv("SERVE_IP")
	serverPort := os.Getenv("SERVE_PORT")
	if serverIP == "" {
		serverIP = "127.0.0.1" // Default to localhost
	}
	if serverPort == "" {
		serverPort = "9000" // Default to port 9000
	}

	// Start server
	serverAddr := fmt.Sprintf("%s:%s", serverIP, serverPort)
	log.Printf("Starting server on %s", serverAddr)
	if err := http.ListenAndServe(serverAddr, sessionManager.LoadAndSave(mux)); err != nil {
		log.Fatal(err)
	}
}
