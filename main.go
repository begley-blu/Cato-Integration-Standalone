package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Configuration holds all the program configuration
type Configuration struct {
	CatoAPIURL     string
	CatoAPIToken   string
	CatoAccountID  string
	SyslogProtocol string
	SyslogServer   string
	SyslogPort     string
	CEFVendor      string
	CEFProduct     string
	CEFVersion     string
	LogLevel       string
	FetchInterval  int
	LastMarker     string
	MarkerFile     string // Path to the file storing the last marker
	FieldMapFile   string // Path to a JSON file containing field mappings
	MaxMsgSize     int    // Maximum size of a syslog message
	Verbose        bool   // Verbose output for debugging
	UseEventIP     bool   // Whether to use IPs from event data as source
	CustomSourceIP string // Custom source IP to use if UseEventIP is false
}

// FieldMapping represents how fields should be mapped and ordered
type FieldMapping struct {
	// OrderedFields is a list of fields in the exact order they should appear
	OrderedFields []string `json:"ordered_fields"`

	// FieldMappings maps source field names to target CEF field names
	FieldMappings map[string]string `json:"field_mappings"`
}

// GraphQLRequest represents a GraphQL API request
type GraphQLRequest struct {
	Query     string                 `json:"query"`
	Variables map[string]interface{} `json:"variables,omitempty"`
}

// EventsFeedResponse represents the response from the events feed
type EventsFeedResponse struct {
	Data struct {
		EventsFeed struct {
			Marker       string `json:"marker"`
			FetchedCount int    `json:"fetchedCount"`
			Accounts     []struct {
				ID          string `json:"id"`
				ErrorString string `json:"errorString"`
				Records     []struct {
					FieldsMap map[string]string `json:"fieldsMap"`
				} `json:"records"`
			} `json:"accounts"`
		} `json:"eventsFeed"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors,omitempty"`
}

// SyslogWriter manages a connection to a syslog server
type SyslogWriter struct {
	protocol string
	address  string
	conn     net.Conn
}

func main() {
	// Load configuration from flags/environment variables
	config := loadConfig()

	// Setup logging
	setupLogging(config.LogLevel)
	log.Println("Starting Cato Networks CEF Forwarder")
	log.Printf("Connecting to API at %s", config.CatoAPIURL)
	log.Printf("Forwarding to syslog server %s:%s via %s", config.SyslogServer, config.SyslogPort, config.SyslogProtocol)
	log.Printf("Polling for new events every %d seconds", config.FetchInterval)

	if config.LastMarker != "" {
		log.Printf("Starting from marker: %s", config.LastMarker)
	}

	// Load field mapping
	fieldMapping := loadFieldMapping(config.FieldMapFile)

	// Initialize syslog writer
	syslogWriter, err := NewSyslogWriter(config.SyslogProtocol, fmt.Sprintf("%s:%s", config.SyslogServer, config.SyslogPort))
	if err != nil {
		log.Fatalf("Failed to initialize syslog connection: %v", err)
	}
	defer syslogWriter.Close()

	// Process events from last marker on startup
	processEvents(&config, fieldMapping, syslogWriter)

	// Setup timer to fetch events periodically
	ticker := time.NewTicker(time.Duration(config.FetchInterval) * time.Second)
	defer ticker.Stop()

	// Setup signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Start the main processing loop
	for {
		select {
		case <-ticker.C:
			processEvents(&config, fieldMapping, syslogWriter)
		case sig := <-sigChan:
			log.Printf("Received signal %v, shutting down...", sig)
			// The marker has already been saved to file by processEvents
			return
		}
	}
}

// NewSyslogWriter creates a new connection to a syslog server
func NewSyslogWriter(protocol, address string) (*SyslogWriter, error) {
	conn, err := net.Dial(protocol, address)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to syslog server: %w", err)
	}

	return &SyslogWriter{
		protocol: protocol,
		address:  address,
		conn:     conn,
	}, nil
}

// Write sends a message to the syslog server
func (w *SyslogWriter) Write(message string) error {
	_, err := fmt.Fprintln(w.conn, message)
	return err
}

// Close closes the connection to the syslog server
func (w *SyslogWriter) Close() error {
	if w.conn != nil {
		return w.conn.Close()
	}
	return nil
}

// Reconnect attempts to reconnect to the syslog server if the connection was lost
func (w *SyslogWriter) Reconnect() error {
	if w.conn != nil {
		w.conn.Close()
	}

	conn, err := net.Dial(w.protocol, w.address)
	if err != nil {
		return fmt.Errorf("failed to reconnect to syslog server: %w", err)
	}

	w.conn = conn
	return nil
}

func loadConfig() Configuration {
	// Define command-line flags with environment variable fallbacks
	apiURL := flag.String("url", getEnvOrDefault("CATO_API_URL", "https://api.catonetworks.com/api/v1/graphql2"), "Cato Networks API URL")
	apiToken := flag.String("token", getEnvOrDefault("CATO_API_TOKEN", ""), "Cato Networks API Token")
	accountID := flag.String("account", getEnvOrDefault("CATO_ACCOUNT_ID", ""), "Cato Networks Account ID")
	syslogProto := flag.String("syslog-proto", getEnvOrDefault("SYSLOG_PROTOCOL", "tcp"), "Syslog protocol (udp/tcp)")
	syslogServer := flag.String("syslog-server", getEnvOrDefault("SYSLOG_SERVER", "localhost"), "Syslog server address")
	syslogPort := flag.String("syslog-port", getEnvOrDefault("SYSLOG_PORT", "514"), "Syslog server port")
	cefVendor := flag.String("cef-vendor", getEnvOrDefault("CEF_VENDOR", "Check Point"), "CEF vendor field")
	cefProduct := flag.String("cef-product", getEnvOrDefault("CEF_PRODUCT", "Cato Networks SASE Platform"), "CEF product field")
	cefVersion := flag.String("cef-version", getEnvOrDefault("CEF_VERSION", "1.0"), "CEF version field")
	logLevel := flag.String("log-level", getEnvOrDefault("LOG_LEVEL", "info"), "Log level (debug, info, warn, error)")
	fetchInterval := flag.Int("interval", getEnvOrIntDefault("FETCH_INTERVAL", 10), "Event fetch interval in seconds")
	markerFile := flag.String("marker-file", getEnvOrDefault("MARKER_FILE", "last_marker.txt"), "File to store the last event marker")
	fieldMapFile := flag.String("field-map", getEnvOrDefault("FIELD_MAP_FILE", "field_map.json"), "JSON file with field mapping configuration")
	maxMsgSize := flag.Int("max-msg-size", getEnvOrIntDefault("MAX_MSG_SIZE", 8096), "Maximum size of a syslog message")
	verbose := flag.Bool("verbose", getEnvOrBoolDefault("VERBOSE", false), "Enable verbose output")
	useEventIP := flag.Bool("use-event-ip", getEnvOrBoolDefault("USE_EVENT_IP", false), "Use IP from event data as source")
	customSourceIP := flag.String("source-ip", getEnvOrDefault("CUSTOM_SOURCE_IP", ""), "Custom source IP for syslog messages")

	flag.Parse()

	// Validate required configuration
	if *apiToken == "" {
		log.Fatal("API Token is required. Set it via CATO_API_TOKEN environment variable or -token flag.")
	}
	if *accountID == "" {
		log.Fatal("Account ID is required. Set it via CATO_ACCOUNT_ID environment variable or -account flag.")
	}

	config := Configuration{
		CatoAPIURL:     *apiURL,
		CatoAPIToken:   *apiToken,
		CatoAccountID:  *accountID,
		SyslogProtocol: *syslogProto,
		SyslogServer:   *syslogServer,
		SyslogPort:     *syslogPort,
		CEFVendor:      *cefVendor,
		CEFProduct:     *cefProduct,
		CEFVersion:     *cefVersion,
		LogLevel:       *logLevel,
		FetchInterval:  *fetchInterval,
		MarkerFile:     *markerFile,
		FieldMapFile:   *fieldMapFile,
		MaxMsgSize:     *maxMsgSize,
		LastMarker:     "", // Start with empty marker, will be loaded from file
		Verbose:        *verbose,
		UseEventIP:     *useEventIP,
		CustomSourceIP: *customSourceIP,
	}

	// Load the last marker from file if it exists
	config.LastMarker = loadMarkerFromFile(config.MarkerFile)
	if config.LastMarker != "" {
		log.Printf("Loaded last marker from file: %s", config.MarkerFile)
	}

	return config
}

func getEnvOrDefault(key, defaultValue string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return defaultValue
}

func getEnvOrIntDefault(key string, defaultValue int) int {
	if value, exists := os.LookupEnv(key); exists {
		var result int
		_, err := fmt.Sscanf(value, "%d", &result)
		if err == nil {
			return result
		}
	}
	return defaultValue
}

func getEnvOrBoolDefault(key string, defaultValue bool) bool {
	if value, exists := os.LookupEnv(key); exists {
		if value == "true" || value == "1" || value == "yes" || value == "y" {
			return true
		}
		if value == "false" || value == "0" || value == "no" || value == "n" {
			return false
		}
	}
	return defaultValue
}

func setupLogging(level string) {
	// Basic logging setup
	log.SetOutput(os.Stdout)
	log.SetFlags(log.LstdFlags | log.Lshortfile)
}

// loadFieldMapping loads field mapping configuration from a JSON file
func loadFieldMapping(filename string) FieldMapping {
	// Default mapping if file doesn't exist or can't be loaded
	defaultMapping := FieldMapping{
		OrderedFields: []string{
			"src", "spt", "dst", "dpt", "proto", "in", "out", "rt", "aid", "sco", "dco",
		},
		FieldMappings: map[string]string{
			"src_ip":      "src",
			"dst_ip":      "dst",
			"src_port":    "spt",
			"dst_port":    "dpt",
			"protocol":    "proto",
			"bytes_in":    "in",
			"bytes_out":   "out",
			"time":        "rt",
			"account_id":  "aid",
			"src_country": "sco",
			"dst_country": "dco",
		},
	}

	// Try to load from file
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		// If file doesn't exist, create it with default mapping
		if os.IsNotExist(err) {
			log.Printf("Field mapping file %s not found, creating with default mapping", filename)
			jsonData, err := json.MarshalIndent(defaultMapping, "", "  ")
			if err == nil {
				err = ioutil.WriteFile(filename, jsonData, 0644)
				if err != nil {
					log.Printf("Failed to create default field mapping file: %v", err)
				}
			}
		} else {
			log.Printf("Error reading field mapping file: %v", err)
		}

		return defaultMapping
	}

	// Parse the JSON file
	var mapping FieldMapping
	if err := json.Unmarshal(data, &mapping); err != nil {
		log.Printf("Error parsing field mapping file: %v, using default mapping", err)
		return defaultMapping
	}

	log.Printf("Loaded field mapping with %d ordered fields and %d mappings",
		len(mapping.OrderedFields), len(mapping.FieldMappings))
	return mapping
}

// loadMarkerFromFile loads the last marker from a file if it exists
func loadMarkerFromFile(filename string) string {
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("Warning: Error reading marker file: %v", err)
		} else {
			log.Printf("Marker file %s does not exist, will create it on first run", filename)
		}
		return ""
	}
	return string(data)
}

// saveMarkerToFile saves the current marker to a file
func saveMarkerToFile(filename string, marker string) error {
	// Create the directory if it doesn't exist
	dir := filepath.Dir(filename)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory for marker file: %w", err)
		}
	}

	return ioutil.WriteFile(filename, []byte(marker), 0644)
}

// fetchEvents fetches events from the Cato API
func fetchEvents(config Configuration) ([]map[string]string, string, error) {
	// Define GraphQL query for events
	query := `query eventsFeed($accountIDs: [ID!]!, $marker: String, $filters: [EventFeedFieldFilterInput!]) {
		eventsFeed(accountIDs: $accountIDs, marker: $marker, filters: $filters) {
			marker
			fetchedCount
			accounts {
				id
				errorString
				records {
					fieldsMap
				}
			}
		}
	}`

	// Create variables for the query
	variables := map[string]interface{}{
		"accountIDs": []string{config.CatoAccountID},
		"filters":    []interface{}{}, // Empty filters - customize if needed
	}

	// Check if marker is empty (first run)
	if config.LastMarker == "" {
		// For empty marker case, we're starting fresh
		// We leave the marker as null/empty which tells the API to start from the beginning
		// or with a reasonable default timeframe
		log.Printf("No marker found, this is the first run - polling from API's default starting point")
		// marker is omitted intentionally here, as leaving it empty/null tells the API
		// to return the oldest available events first
	} else {
		// Use the existing marker for subsequent polls
		variables["marker"] = config.LastMarker

		if config.LogLevel == "debug" {
			log.Printf("Fetching events with marker: %s", config.LastMarker)
		}
	}

	// Prepare the request
	req := GraphQLRequest{
		Query:     query,
		Variables: variables,
	}
	reqBody, err := json.Marshal(req)
	if err != nil {
		return nil, "", fmt.Errorf("error marshaling request: %w", err)
	}

	// Create HTTP request
	httpReq, err := http.NewRequest("POST", config.CatoAPIURL, bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, "", fmt.Errorf("error creating request: %w", err)
	}

	// Add headers
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", config.CatoAPIToken))

	if config.Verbose {
		log.Printf("Sending request to %s with payload: %s", config.CatoAPIURL, string(reqBody))
	}

	// Send the request
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, "", fmt.Errorf("error sending request: %w", err)
	}
	defer resp.Body.Close()
	// Log the response status code
	log.Printf("API response status: %s (%d)", resp.Status, resp.StatusCode)

	// Read the response
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("error reading response: %w", err)
	}

	// Check for HTTP errors
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("API returned non-OK status: %d, body: %s", resp.StatusCode, string(body))
	}

	// Parse the response
	var response EventsFeedResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, "", fmt.Errorf("error unmarshaling response: %w", err)
	}

	// Check for GraphQL errors
	if len(response.Errors) > 0 {
		return nil, "", fmt.Errorf("GraphQL error: %s", response.Errors[0].Message)
	}

	// Log response details
	log.Printf("API response: Marker: %s, FetchedCount: %d",
		response.Data.EventsFeed.Marker,
		response.Data.EventsFeed.FetchedCount)

	// Log account details
	for _, account := range response.Data.EventsFeed.Accounts {
		if account.ErrorString != "" {
			log.Printf("Error for account %s: %s", account.ID, account.ErrorString)
		} else {
			log.Printf("Account %s: %d records", account.ID, len(account.Records))
		}
	}

	// Extract all records from all accounts
	var allRecords []map[string]string
	for _, account := range response.Data.EventsFeed.Accounts {
		if account.ErrorString != "" {
			log.Printf("Error for account %s: %s", account.ID, account.ErrorString)
			continue
		}

		for _, record := range account.Records {
			allRecords = append(allRecords, record.FieldsMap)
		}
	}

	return allRecords, response.Data.EventsFeed.Marker, nil
}

// forwardEvents processes and forwards events to the syslog server
func forwardEvents(events []map[string]string, config *Configuration, fieldMapping FieldMapping, syslogWriter *SyslogWriter) (int, error) {
	// Count of successfully forwarded events
	var forwardedCount int

	// Process each event
	for _, fieldsMap := range events {
		// Determine source IP based on configuration
		var sourceIP string

		if config.UseEventIP {
			// Extract the client_ip if present - this will be our "source" IP
			sourceIP = getMapValue(fieldsMap, "client_ip", "")
			if sourceIP == "" {
				// Try other potential source IP fields if client_ip is not present
				sourceIP = getMapValue(fieldsMap, "src_ip", "")
				if sourceIP == "" {
					sourceIP = getMapValue(fieldsMap, "src_isp_ip", "")
					if sourceIP == "" {
						sourceIP = getMapValue(fieldsMap, "host_ip", "")
					}
				}
			}

			// If no source IP found in the event, use a default
			if sourceIP == "" {
				sourceIP = "unknown-host"
			}
		} else if config.CustomSourceIP != "" {
			// Use the configured custom source IP
			sourceIP = config.CustomSourceIP
		} else {
			// Default to the system hostname if neither option is configured
			hostname, err := os.Hostname()
			if err != nil {
				sourceIP = "unknown-host"
				if config.LogLevel == "debug" {
					log.Printf("Could not determine hostname: %v, using 'unknown-host'", err)
				}
			} else {
				sourceIP = hostname
			}
		}

		// Format the event as CEF
		cefMessage := formatEventAsCEF(fieldsMap, config, fieldMapping)

		// Format as syslog message with sourceIP as hostname
		syslogMessage := formatSyslogMessage(sourceIP, cefMessage)

		// Check message size
		if len(syslogMessage) > config.MaxMsgSize {
			if config.LogLevel == "debug" {
				log.Printf("Message too large (%d bytes), truncating to %d bytes",
					len(syslogMessage), config.MaxMsgSize)
			}
			// Truncate if too large
			syslogMessage = syslogMessage[:config.MaxMsgSize]
		}

		// Send the message
		err := syslogWriter.Write(syslogMessage)
		if err != nil {
			// Try to reconnect once
			log.Printf("Syslog write failed, attempting to reconnect: %v", err)
			if reconnectErr := syslogWriter.Reconnect(); reconnectErr != nil {
				return forwardedCount, fmt.Errorf("failed to reconnect to syslog server: %w", reconnectErr)
			}

			// Try again after reconnecting
			if err = syslogWriter.Write(syslogMessage); err != nil {
				return forwardedCount, fmt.Errorf("failed to send message after reconnecting: %w", err)
			}
		}

		forwardedCount++
	}

	return forwardedCount, nil
}

// processEvents fetches and processes events from the Cato API
func processEvents(config *Configuration, fieldMapping FieldMapping, syslogWriter *SyslogWriter) {
	// Fetch events from Cato API
	log.Printf("Fetching events from API %s with account ID %s", config.CatoAPIURL, config.CatoAccountID)
	events, newMarker, err := fetchEvents(*config)
	if err != nil {
		log.Printf("Error fetching events: %v", err)
		return
	}

	log.Printf("API returned marker: %s", newMarker)

	// Always update and save the marker, even if there are no events
	if newMarker != "" && newMarker != config.LastMarker {
		config.LastMarker = newMarker

		// Save the marker to file
		if err := saveMarkerToFile(config.MarkerFile, newMarker); err != nil {
			log.Printf("Warning: Failed to save marker to file: %v", err)
		} else if config.LogLevel == "debug" {
			log.Printf("Saved marker to file: %s", config.MarkerFile)
		}
	}

	if len(events) > 0 {
		// Process and forward events
		log.Printf("Fetched %d events", len(events))

		// For first run (no marker), limit the number of events processed to avoid flooding
		if config.LastMarker == "" {
			// If we received a massive amount of events in the first batch, we might want to limit them
			if len(events) > 1000 {
				log.Printf("First run - limiting events to most recent 1000 to avoid flooding")
				events = events[len(events)-1000:]
			} else {
				log.Printf("First run - processing %d initial events", len(events))
			}
		}

		// Count forwarded events
		forwarded, err := forwardEvents(events, config, fieldMapping, syslogWriter)
		if err != nil {
			log.Printf("Error forwarding events: %v", err)
		} else {
			log.Printf("Forwarded %d events to syslog server", forwarded)
		}
	} else {
		log.Println("No events returned from API")

		if config.LogLevel == "debug" {
			log.Println("No new events found")
		}
		// Update the marker for the next fetch
		config.LastMarker = newMarker

		// Save the marker to file
		if err := saveMarkerToFile(config.MarkerFile, newMarker); err != nil {
			log.Printf("Warning: Failed to save marker to file: %v", err)
		} else if config.LogLevel == "debug" {
			log.Printf("Saved marker to file: %s", config.MarkerFile)
		}
	}
}

// formatEventAsCEF formats an event as a CEF message
func formatEventAsCEF(fieldsMap map[string]string, config *Configuration, fieldMapping FieldMapping) string {
	// Create CEF signature and name from event type and subtype
	signature := getMapValue(fieldsMap, "event_type", "Unknown")
	name := fmt.Sprintf("%s - %s",
		signature,
		getMapValue(fieldsMap, "event_sub_type", "Unknown"))

	// Determine CEF severity (0-10) based on event type
	severity := mapEventTypeToSeverity(signature)

	// Create a map for all CEF extensions
	extensions := make(map[string]string)

	// Map fields according to the field mapping configuration
	for sourceKey, targetKey := range fieldMapping.FieldMappings {
		if value, exists := fieldsMap[sourceKey]; exists {
			extensions[targetKey] = value
		} else {
			extensions[targetKey] = ""
		}
	}

	// Add any remaining fields with their original keys
	for k, v := range fieldsMap {
		// Skip fields we've already mapped
		if isMappedField(k, fieldMapping.FieldMappings) {
			continue
		}
		extensions[k] = v
	}

	// Format the CEF header
	header := fmt.Sprintf("CEF:0|%s|%s|%s|%s|%s|%d|",
		config.CEFVendor, config.CEFProduct, config.CEFVersion, signature, name, severity)

	// Format the extensions with controlled ordering
	var extensionParts []string

	// First add the ordered fields in the exact order specified
	for _, fieldName := range fieldMapping.OrderedFields {
		if value, exists := extensions[fieldName]; exists {
			// Format the field without quotes
			extensionParts = append(extensionParts, fmt.Sprintf("%s=%s", fieldName, value))

			// Remove the field from the map so we don't add it again
			delete(extensions, fieldName)
		}
	}

	// Then add any remaining fields
	var remainingFields []string
	for k := range extensions {
		remainingFields = append(remainingFields, k)
	}

	// Sort the remaining fields alphabetically for consistent output
	sort.Strings(remainingFields)

	// Add the remaining fields to the output
	for _, fieldName := range remainingFields {
		value := extensions[fieldName]
		extensionParts = append(extensionParts, fmt.Sprintf("%s=%s", fieldName, value))
	}

	// Combine everything into a single CEF message
	return header + strings.Join(extensionParts, " ")
}

// formatSyslogMessage formats a CEF message as a syslog message
func formatSyslogMessage(hostname, message string) string {
	// Format syslog priority and timestamp
	priority := "134" // Corresponds to local0.info
	timestamp := time.Now().Format("Jan  2 15:04:05")

	// Format the complete syslog message
	return fmt.Sprintf("<%s>%s %s %s", priority, timestamp, hostname, message)
}

func getMapValue(m map[string]string, key, defaultVal string) string {
	if val, ok := m[key]; ok && val != "" {
		return val
	}
	return defaultVal
}

func isMappedField(fieldName string, fieldMappings map[string]string) bool {
	for sourceKey := range fieldMappings {
		if sourceKey == fieldName {
			return true
		}
	}
	return false
}

// isAlreadyQuoted checks if a string is already enclosed in double quotes
func isAlreadyQuoted(s string) bool {
	// At minimum, a quoted string needs at least 2 characters: the opening and closing quotes
	if len(s) < 2 {
		return false
	}

	// Check if the string starts with a double quote and ends with a double quote
	return s[0] == '"' && s[len(s)-1] == '"'
}

func mapEventTypeToSeverity(eventType string) int {
	// Map event types to CEF severity levels (0-10)
	// This is a simple example - customize based on your requirements
	severityMap := map[string]int{
		"Threat":       9,
		"Error":        8,
		"Alert":        7,
		"Warning":      6,
		"Connectivity": 5,
		"Traffic":      4,
		"Info":         3,
		"Debug":        2,
	}

	if severity, exists := severityMap[eventType]; exists {
		return severity
	}

	// Default severity for unknown event types
	return 5
}
