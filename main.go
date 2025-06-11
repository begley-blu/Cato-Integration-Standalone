package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
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
	CatoAPIURL      string
	CatoAPIKey      string
	CatoAccountID   string
	SyslogProtocol  string
	SyslogServer    string
	SyslogPort      string
	CEFVendor       string
	CEFProduct      string
	CEFVersion      string
	LogLevel        string
	FetchInterval   int
	LastMarker      string
	MarkerFile      string
	FieldMapFile    string
	MaxMsgSize      int
	Verbose         bool
	UseEventIP      bool
	CustomSourceIP  string
	MaxEvents       int
	RetryAttempts   int
	RetryDelay      int
	MaxPagination   int
	HealthCheckPort int
	LogToSyslog     bool
	LogFile         string
	MaxBackoffDelay int
	ConnTimeout     int
}

// FieldMapping represents how fields should be mapped and ordered
type FieldMapping struct {
	OrderedFields []string          `json:"ordered_fields"`
	FieldMappings map[string]string `json:"field_mappings"`
	CEFVendor     string            `json:"cef_vendor"`
	CEFProduct    string            `json:"cef_product"`
	CEFVersion    string            `json:"cef_version"`
}

// GraphQLRequest represents a GraphQL API request
type GraphQLRequest struct {
	Query     string                 `json:"query"`
	Variables map[string]interface{} `json:"variables,omitempty"`
}

// EventsFeedResponse based on current API schema
type EventsFeedResponse struct {
	Data struct {
		EventsFeed struct {
			Marker       *string `json:"marker"`
			FetchedCount int     `json:"fetchedCount"`
			HasMore      bool    `json:"hasMore"`
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
		Message   string `json:"message"`
		Locations []struct {
			Line   int `json:"line"`
			Column int `json:"column"`
		} `json:"locations,omitempty"`
		Path       []string               `json:"path,omitempty"`
		Extensions map[string]interface{} `json:"extensions,omitempty"`
	} `json:"errors,omitempty"`
}

// SyslogWriter manages a resilient connection to a syslog server
type SyslogWriter struct {
	protocol       string
	address        string
	conn           net.Conn
	reconnectCount int
	lastReconnect  time.Time
	maxReconnects  int
	reconnectDelay time.Duration
}

// ServiceStats tracks service health metrics
type ServiceStats struct {
	StartTime            time.Time
	LastSuccessfulRun    time.Time
	TotalEventsForwarded int64
	TotalAPIRequests     int64
	FailedAPIRequests    int64
	SyslogReconnects     int64
	LastError            string
	LastErrorTime        time.Time
	MarkerFileUpdates    int64
}

var (
	serviceStats = &ServiceStats{StartTime: time.Now()}
	ctx          context.Context
	cancel       context.CancelFunc
)

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func main() {
	// Create cancellable context for graceful shutdown
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()

	config := loadConfig()

	// Setup proper logging for systemd service
	if err := setupLogging(config); err != nil {
		log.Fatalf("Failed to setup logging: %v", err)
	}

	log.Println("Starting Cato Networks CEF Forwarder v3.1 (Systemd Ready)")
	log.Printf("PID: %d", os.Getpid())
	log.Printf("API Endpoint: %s", config.CatoAPIURL)
	log.Printf("Account ID: %s", config.CatoAccountID)
	log.Printf("Syslog Target: %s:%s (%s)", config.SyslogServer, config.SyslogPort, config.SyslogProtocol)
	log.Printf("Poll Interval: %ds", config.FetchInterval)
	log.Printf("Max Events per Request: %d", config.MaxEvents)
	log.Printf("Max Pagination Requests: %d", config.MaxPagination)

	// Validate configuration
	if err := validateConfig(config); err != nil {
		log.Fatalf("Configuration validation failed: %v", err)
	}

	if config.LastMarker != "" {
		log.Printf("Starting from saved marker")
	} else {
		log.Printf("Starting fresh - will collect from oldest available events")
	}

	fieldMapping := loadFieldMapping(config.FieldMapFile)

	// Initialize syslog writer with resilience
	syslogWriter, err := NewSyslogWriter(config.SyslogProtocol,
		fmt.Sprintf("%s:%s", config.SyslogServer, config.SyslogPort), config)
	if err != nil {
		log.Fatalf("Failed to initialize syslog connection: %v", err)
	}
	defer syslogWriter.Close()

	// Start health check server if configured
	if config.HealthCheckPort > 0 {
		go startHealthCheckServer(config.HealthCheckPort)
	}

	// Setup signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGHUP)

	// Main service loop with exponential backoff on failures
	ticker := time.NewTicker(time.Duration(config.FetchInterval) * time.Second)
	defer ticker.Stop()

	backoffDelay := 1 * time.Second
	maxBackoff := time.Duration(config.MaxBackoffDelay) * time.Second

	// Process initial events
	processEventsWithRecovery(&config, fieldMapping, syslogWriter)

	for {
		select {
		case <-ctx.Done():
			log.Println("Context cancelled, shutting down...")
			return

		case <-ticker.C:
			success := processEventsWithRecovery(&config, fieldMapping, syslogWriter)

			if success {
				// Reset backoff on success
				backoffDelay = 1 * time.Second
				ticker.Reset(time.Duration(config.FetchInterval) * time.Second)
				serviceStats.LastSuccessfulRun = time.Now()
			} else {
				// Apply exponential backoff on failure
				log.Printf("Processing failed, backing off for %v", backoffDelay)
				ticker.Reset(backoffDelay)
				backoffDelay *= 2
				if backoffDelay > maxBackoff {
					backoffDelay = maxBackoff
				}
			}

		case sig := <-sigChan:
			log.Printf("Received signal %v, initiating graceful shutdown...", sig)

			if sig == syscall.SIGHUP {
				log.Println("SIGHUP received - reloading configuration")
				// Reload field mapping
				fieldMapping = loadFieldMapping(config.FieldMapFile)
				log.Println("Configuration reloaded")
				continue
			}

			// Save final marker and shutdown
			log.Println("Saving final state and shutting down...")
			cancel()
			return
		}
	}
}

func NewSyslogWriter(protocol, address string, config Configuration) (*SyslogWriter, error) {
	conn, err := net.DialTimeout(protocol, address, time.Duration(config.ConnTimeout)*time.Second)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to syslog server: %w", err)
	}

	return &SyslogWriter{
		protocol:       protocol,
		address:        address,
		conn:           conn,
		maxReconnects:  10,
		reconnectDelay: 5 * time.Second,
	}, nil
}

func (w *SyslogWriter) Write(message string) error {
	if w.conn == nil {
		return fmt.Errorf("no connection available")
	}

	_, err := fmt.Fprintln(w.conn, message)
	return err
}

func (w *SyslogWriter) Close() error {
	if w.conn != nil {
		return w.conn.Close()
	}
	return nil
}

func (w *SyslogWriter) Reconnect() error {
	// Implement connection rate limiting
	if time.Since(w.lastReconnect) < w.reconnectDelay {
		return fmt.Errorf("reconnection rate limited")
	}

	if w.reconnectCount >= w.maxReconnects {
		return fmt.Errorf("max reconnection attempts exceeded")
	}

	if w.conn != nil {
		w.conn.Close()
	}

	conn, err := net.DialTimeout(w.protocol, w.address, 30*time.Second)
	if err != nil {
		w.reconnectCount++
		w.lastReconnect = time.Now()
		serviceStats.SyslogReconnects++
		return fmt.Errorf("failed to reconnect to syslog server: %w", err)
	}

	w.conn = conn
	w.reconnectCount = 0 // Reset on successful reconnection
	w.lastReconnect = time.Now()
	log.Printf("Successfully reconnected to syslog server")
	return nil
}

func loadConfig() Configuration {
	apiURL := flag.String("url", getEnvOrDefault("CATO_API_URL", "https://api.catonetworks.com/api/v1/graphql2"), "Cato Networks API URL")
	apiKey := flag.String("api-key", getEnvOrDefault("CATO_API_KEY", ""), "Cato Networks API Key")
	accountID := flag.String("account", getEnvOrDefault("CATO_ACCOUNT_ID", ""), "Cato Networks Account ID")
	syslogProto := flag.String("syslog-proto", getEnvOrDefault("SYSLOG_PROTOCOL", "tcp"), "Syslog protocol (udp/tcp)")
	syslogServer := flag.String("syslog-server", getEnvOrDefault("SYSLOG_SERVER", "localhost"), "Syslog server address")
	syslogPort := flag.String("syslog-port", getEnvOrDefault("SYSLOG_PORT", "514"), "Syslog server port")
	cefVendor := flag.String("cef-vendor", getEnvOrDefault("CEF_VENDOR", "Cato Networks"), "CEF vendor field")
	cefProduct := flag.String("cef-product", getEnvOrDefault("CEF_PRODUCT", "SASE Platform"), "CEF product field")
	cefVersion := flag.String("cef-version", getEnvOrDefault("CEF_VERSION", "1.0"), "CEF version field")
	logLevel := flag.String("log-level", getEnvOrDefault("LOG_LEVEL", "info"), "Log level (debug, info, warn, error)")
	fetchInterval := flag.Int("interval", getEnvOrIntDefault("FETCH_INTERVAL", 60), "Event fetch interval in seconds")
	markerFile := flag.String("marker-file", getEnvOrDefault("MARKER_FILE", "/var/lib/cato-forwarder/last_marker.txt"), "File to store the last event marker")
	fieldMapFile := flag.String("field-map", getEnvOrDefault("FIELD_MAP_FILE", "/etc/cato-forwarder/field_map.json"), "JSON file with field mapping configuration")
	maxMsgSize := flag.Int("max-msg-size", getEnvOrIntDefault("MAX_MSG_SIZE", 8192), "Maximum size of a syslog message")
	maxEvents := flag.Int("max-events", getEnvOrIntDefault("MAX_EVENTS", 5000), "Maximum events per API request")
	verbose := flag.Bool("verbose", getEnvOrBoolDefault("VERBOSE", false), "Enable verbose output")
	useEventIP := flag.Bool("use-event-ip", getEnvOrBoolDefault("USE_EVENT_IP", false), "Use IP from event data as source")
	customSourceIP := flag.String("source-ip", getEnvOrDefault("CUSTOM_SOURCE_IP", ""), "Custom source IP for syslog messages")
	retryAttempts := flag.Int("retry-attempts", getEnvOrIntDefault("RETRY_ATTEMPTS", 3), "Number of retry attempts for API calls")
	retryDelay := flag.Int("retry-delay", getEnvOrIntDefault("RETRY_DELAY", 5), "Delay between retries in seconds")
	maxPagination := flag.Int("max-pagination", getEnvOrIntDefault("MAX_PAGINATION", 50), "Maximum pagination requests per cycle")
	healthCheckPort := flag.Int("health-port", getEnvOrIntDefault("HEALTH_CHECK_PORT", 8080), "Health check HTTP server port (0 to disable)")
	logToSyslog := flag.Bool("log-syslog", getEnvOrBoolDefault("LOG_TO_SYSLOG", false), "Log to system syslog")
	logFile := flag.String("log-file", getEnvOrDefault("LOG_FILE", ""), "Log file path (empty for stdout)")
	maxBackoffDelay := flag.Int("max-backoff", getEnvOrIntDefault("MAX_BACKOFF_DELAY", 300), "Maximum backoff delay in seconds")
	connTimeout := flag.Int("conn-timeout", getEnvOrIntDefault("CONNECTION_TIMEOUT", 30), "Connection timeout in seconds")

	flag.Parse()

	if *maxEvents > 5000 {
		*maxEvents = 5000
	}

	return Configuration{
		CatoAPIURL:      *apiURL,
		CatoAPIKey:      *apiKey,
		CatoAccountID:   *accountID,
		SyslogProtocol:  *syslogProto,
		SyslogServer:    *syslogServer,
		SyslogPort:      *syslogPort,
		CEFVendor:       *cefVendor,
		CEFProduct:      *cefProduct,
		CEFVersion:      *cefVersion,
		LogLevel:        *logLevel,
		FetchInterval:   *fetchInterval,
		MarkerFile:      *markerFile,
		FieldMapFile:    *fieldMapFile,
		MaxMsgSize:      *maxMsgSize,
		MaxEvents:       *maxEvents,
		Verbose:         *verbose,
		UseEventIP:      *useEventIP,
		CustomSourceIP:  *customSourceIP,
		RetryAttempts:   *retryAttempts,
		RetryDelay:      *retryDelay,
		MaxPagination:   *maxPagination,
		HealthCheckPort: *healthCheckPort,
		LogToSyslog:     *logToSyslog,
		LogFile:         *logFile,
		MaxBackoffDelay: *maxBackoffDelay,
		ConnTimeout:     *connTimeout,
		LastMarker:      loadMarkerFromFile(*markerFile),
	}
}

func validateConfig(config Configuration) error {
	missing := []string{}
	if config.CatoAPIKey == "" {
		log.Printf("Missing required config: CATO_API_KEY (or --api-key)")
		missing = append(missing, "CATO_API_KEY")
	}
	if config.CatoAccountID == "" {
		log.Printf("Missing required config: CATO_ACCOUNT_ID (or --account)")
		missing = append(missing, "CATO_ACCOUNT_ID")
	}
	if config.SyslogServer == "" {
		log.Printf("Missing required config: SYSLOG_SERVER (or --syslog-server)")
		missing = append(missing, "SYSLOG_SERVER")
	}
	if config.SyslogPort == "" {
		log.Printf("Missing required config: SYSLOG_PORT (or --syslog-port)")
		missing = append(missing, "SYSLOG_PORT")
	}
	if config.SyslogProtocol == "" {
		log.Printf("Missing required config: SYSLOG_PROTOCOL (or --syslog-proto)")
		missing = append(missing, "SYSLOG_PROTOCOL")
	}
	if config.FieldMapFile == "" {
		log.Printf("Missing required config: FIELD_MAP_FILE (or --field-map)")
		missing = append(missing, "FIELD_MAP_FILE")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required configuration: %v", missing)
	}
	if config.FetchInterval < 10 {
		return fmt.Errorf("fetch interval must be at least 10 seconds")
	}
	if config.MaxEvents < 1 || config.MaxEvents > 5000 {
		return fmt.Errorf("max events must be between 1 and 5000")
	}
	return nil
}

func setupLogging(config Configuration) error {
	var writers []io.Writer

	// Always include stdout for systemd journal
	writers = append(writers, os.Stdout)

	// Optional log file
	if config.LogFile != "" {
		dir := filepath.Dir(config.LogFile)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create log directory: %w", err)
		}

		file, err := os.OpenFile(config.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return fmt.Errorf("failed to open log file: %w", err)
		}
		writers = append(writers, file)
	}

	// Optional system syslog
	if config.LogToSyslog {
		log.Printf("Warning: LogToSyslog is enabled, but no syslog writer is implemented. Skipping syslog logging.")
		// To support syslog logging, implement a custom io.Writer for syslog and append it here.
	}

	if len(writers) > 1 {
		log.SetOutput(io.MultiWriter(writers...))
	} else {
		log.SetOutput(writers[0])
	}

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	return nil
}

// Health check endpoint for systemd and monitoring
func startHealthCheckServer(port int) {
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		status := map[string]interface{}{
			"status":              "healthy",
			"uptime":              time.Since(serviceStats.StartTime).String(),
			"last_successful_run": serviceStats.LastSuccessfulRun.Format(time.RFC3339),
			"total_events":        serviceStats.TotalEventsForwarded,
			"total_api_requests":  serviceStats.TotalAPIRequests,
			"failed_api_requests": serviceStats.FailedAPIRequests,
			"syslog_reconnects":   serviceStats.SyslogReconnects,
			"last_error":          serviceStats.LastError,
			"last_error_time":     serviceStats.LastErrorTime.Format(time.RFC3339),
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(status)
	})

	http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "cato_forwarder_uptime_seconds %d\n", int64(time.Since(serviceStats.StartTime).Seconds()))
		fmt.Fprintf(w, "cato_forwarder_total_events %d\n", serviceStats.TotalEventsForwarded)
		fmt.Fprintf(w, "cato_forwarder_api_requests_total %d\n", serviceStats.TotalAPIRequests)
		fmt.Fprintf(w, "cato_forwarder_api_requests_failed %d\n", serviceStats.FailedAPIRequests)
		fmt.Fprintf(w, "cato_forwarder_syslog_reconnects %d\n", serviceStats.SyslogReconnects)
	})

	server := &http.Server{
		Addr:         fmt.Sprintf(":%d", port),
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}

	log.Printf("Health check server starting on port %d", port)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("Health check server error: %v", err)
	}
}

// Wrapper for event processing with panic recovery
func processEventsWithRecovery(config *Configuration, fieldMapping FieldMapping, syslogWriter *SyslogWriter) bool {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("PANIC recovered in processEvents: %v", r)
			serviceStats.LastError = fmt.Sprintf("PANIC: %v", r)
			serviceStats.LastErrorTime = time.Now()
		}
	}()

	err := processAllEvents(config, fieldMapping, syslogWriter)
	if err != nil {
		log.Printf("Error processing events: %v", err)
		serviceStats.LastError = err.Error()
		serviceStats.LastErrorTime = time.Now()
		serviceStats.FailedAPIRequests++
		return false
	}

	return true
}

func processAllEvents(config *Configuration, fieldMapping FieldMapping, syslogWriter *SyslogWriter) error {
	totalEventsProcessed := 0
	paginationCount := 0
	currentMarker := config.LastMarker

	serviceStats.TotalAPIRequests++

	// Track poll period
	pollStart := time.Now()
	pollEnd := pollStart
	numErrors := 0
	totalRetryErrors := 0
	recoveries := 0

	// Wrap fetchEventsPage to count errors and recoveries
	fetchEventsPageWithStats := func(config Configuration, marker string) ([]map[string]string, string, bool, error) {
		var lastErr error
		for attempt := 0; attempt <= config.RetryAttempts; attempt++ {
			if attempt > 0 {
				delay := time.Duration(config.RetryDelay) * time.Second
				log.Printf("Retry attempt %d/%d after %v", attempt, config.RetryAttempts, delay)
				time.Sleep(delay)
			}

			events, newMarker, hasMore, err := performAPIRequest(config, buildGraphQLRequest(config, marker))
			if err == nil {
				if attempt > 0 {
					recoveries++
				}
				return events, newMarker, hasMore, nil
			}

			totalRetryErrors++
			lastErr = err
			log.Printf("API request attempt %d failed: %v", attempt+1, err)
		}
		return nil, "", false, fmt.Errorf("all retry attempts failed, last error: %w", lastErr)
	}

	for paginationCount < config.MaxPagination {
		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled during pagination")
		default:
		}

		events, newMarker, hasMore, err := fetchEventsPageWithStats(*config, currentMarker)

		if err != nil {
			numErrors++
			log.Printf("Error fetching events page %d: %v", paginationCount+1, err)
			continue // skip this page, but continue polling
		}

		paginationCount++
		pollEnd = time.Now()

		if len(events) > 0 {
			forwarded, err := forwardEvents(events, config, fieldMapping, syslogWriter)
			if err != nil {
				numErrors++
				log.Printf("Error forwarding events on page %d: %v", paginationCount, err)
				continue // skip this page, but continue polling
			}
			totalEventsProcessed += forwarded
			serviceStats.TotalEventsForwarded += int64(forwarded)
		}

		if newMarker != "" && newMarker != currentMarker {
			currentMarker = newMarker
			config.LastMarker = newMarker
			if err := saveMarkerToFile(config.MarkerFile, newMarker); err != nil {
				numErrors++
				log.Printf("Error saving marker file: %v", err)
			} else {
				serviceStats.MarkerFileUpdates++
			}
		} else if newMarker == currentMarker {
			break
		}

		if !hasMore {
			break
		}

		if paginationCount < config.MaxPagination {
			time.Sleep(100 * time.Millisecond)
		}
	}

	if paginationCount >= config.MaxPagination {
		// No need to log this in summary, but can increment error count if you want
	}

	// Calculate poll period and events/sec
	periodStart := pollStart.Unix()
	periodEnd := pollEnd.Unix()
	eventsPerSecond := 0.0
	if pollEnd.After(pollStart) && totalEventsProcessed > 0 {
		eventsPerSecond = float64(totalEventsProcessed) / pollEnd.Sub(pollStart).Seconds()
	}

	log.Printf("Last poll period: %d - %d. Number of events processed: %d, total events processed since start of run: %d, number of events processed per second: %.2f, number of errors encountered: %d, number of retry errors: %d, number of recoveries: %d, marker file updates: %d",
		periodStart, periodEnd, totalEventsProcessed, serviceStats.TotalEventsForwarded, eventsPerSecond, numErrors, totalRetryErrors, recoveries, serviceStats.MarkerFileUpdates)

	return nil
}

// Helper to build GraphQL request body for fetchEventsPageWithStats
func buildGraphQLRequest(config Configuration, marker string) []byte {
	query := `query eventsFeed($accountIDs: [ID!]!, $marker: String) {
		eventsFeed(accountIDs: $accountIDs, marker: $marker) {
			marker
			accounts {
				id
				errorString
				records {
					fieldsMap
				}
			}
		}
	}`
	variables := map[string]interface{}{
		"accountIDs": []string{config.CatoAccountID},
	}
	if marker != "" {
		variables["marker"] = marker
	}
	req := GraphQLRequest{
		Query:     query,
		Variables: variables,
	}
	reqBody, _ := json.Marshal(req)
	return reqBody
}

func performAPIRequest(config Configuration, reqBody []byte) ([]map[string]string, string, bool, error) {
	httpReq, err := http.NewRequest("POST", config.CatoAPIURL, bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, "", false, fmt.Errorf("error creating request: %w", err)
	}

	// Correct headers based on current API requirements
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", config.CatoAPIKey)
	httpReq.Header.Set("User-Agent", "Cato-CEF-Forwarder/3.1")

	client := &http.Client{Timeout: 120 * time.Second} // Increased timeout for large responses

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, "", false, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, "", false, fmt.Errorf("error reading response: %w", err)
	}

	// Enhanced error handling
	if resp.StatusCode != http.StatusOK {
		switch resp.StatusCode {
		case 401:
			return nil, "", false, fmt.Errorf("authentication failed (401) - check your API key")
		case 403:
			return nil, "", false, fmt.Errorf("access forbidden (403) - ensure Events Integration is enabled and API key has eventsFeed permissions")
		case 429:
			return nil, "", false, fmt.Errorf("rate limit exceeded (429) - reduce polling frequency or maxEvents")
		case 500, 502, 503, 504:
			return nil, "", false, fmt.Errorf("server error (%d) - Cato API experiencing issues", resp.StatusCode)
		default:
			return nil, "", false, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
		}
	}

	var response EventsFeedResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, "", false, fmt.Errorf("error parsing JSON response: %w", err)
	}

	if len(response.Errors) > 0 {
		errorMsg := response.Errors[0].Message
		return nil, "", false, fmt.Errorf("GraphQL error: %s", errorMsg)
	}

	// Handle nullable marker correctly
	var newMarker string
	if response.Data.EventsFeed.Marker != nil {
		newMarker = *response.Data.EventsFeed.Marker
	}

	// Extract records from all accounts
	var allRecords []map[string]string

	for _, account := range response.Data.EventsFeed.Accounts {
		if account.ErrorString != "" {
			log.Printf("Account %s error: %s", account.ID, account.ErrorString)
			continue
		}

		for _, record := range account.Records {
			allRecords = append(allRecords, record.FieldsMap)
		}
	}

	return allRecords, newMarker, response.Data.EventsFeed.HasMore, nil
}

func forwardEvents(events []map[string]string, config *Configuration, fieldMapping FieldMapping, syslogWriter *SyslogWriter) (int, error) {
	var forwardedCount int

	for _, fieldsMap := range events {
		var sourceIP string

		if config.UseEventIP {
			sourceIP = extractSourceIP(fieldsMap)
			if sourceIP == "" {
				sourceIP = "unknown-host"
			}
		} else if config.CustomSourceIP != "" {
			sourceIP = config.CustomSourceIP
		} else {
			if hostname, err := os.Hostname(); err == nil {
				sourceIP = hostname
			} else {
				sourceIP = "cato-forwarder"
			}
		}

		cefMessage := formatEventAsCEF(fieldsMap, config, fieldMapping)
		syslogMessage := formatSyslogMessage(sourceIP, cefMessage)

		if len(syslogMessage) > config.MaxMsgSize {
			if config.Verbose {
				log.Printf("Truncating oversized message: %d -> %d bytes",
					len(syslogMessage), config.MaxMsgSize)
			}
			syslogMessage = syslogMessage[:config.MaxMsgSize]
		}

		if err := syslogWriter.Write(syslogMessage); err != nil {
			log.Printf("Syslog write failed, attempting reconnect: %v", err)
			if reconnectErr := syslogWriter.Reconnect(); reconnectErr != nil {
				return forwardedCount, fmt.Errorf("reconnection failed: %w", reconnectErr)
			}

			if err = syslogWriter.Write(syslogMessage); err != nil {
				return forwardedCount, fmt.Errorf("write failed after reconnect: %w", err)
			}
		}

		forwardedCount++
	}

	return forwardedCount, nil
}

func extractSourceIP(fieldsMap map[string]string) string {
	candidates := []string{"client_ip", "src_ip", "source_ip", "host_ip", "user_ip"}

	for _, field := range candidates {
		if ip := getMapValue(fieldsMap, field, ""); ip != "" {
			return ip
		}
	}

	return ""
}

func formatEventAsCEF(fieldsMap map[string]string, config *Configuration, fieldMapping FieldMapping) string {
	signature := getMapValue(fieldsMap, "event_type", "Unknown")
	name := fmt.Sprintf("%s - %s",
		signature,
		getMapValue(fieldsMap, "event_sub_type", "Unknown"))

	severity := mapEventTypeToSeverity(signature)

	vendor := config.CEFVendor
	if fieldMapping.CEFVendor != "" {
		vendor = fieldMapping.CEFVendor
	}
	product := config.CEFProduct
	if fieldMapping.CEFProduct != "" {
		product = fieldMapping.CEFProduct
	}
	version := config.CEFVersion
	if fieldMapping.CEFVersion != "" {
		version = fieldMapping.CEFVersion
	}

	header := fmt.Sprintf("CEF:0|%s|%s|%s|%s|%s|%d|",
		vendor, product, version,
		signature, name, severity)

	extensions := make(map[string]string)

	// Apply field mappings
	for sourceKey, targetKey := range fieldMapping.FieldMappings {
		if value, exists := fieldsMap[sourceKey]; exists && value != "" {
			extensions[targetKey] = sanitizeCEFValue(value)
		}
	}

	// Add unmapped fields
	for k, v := range fieldsMap {
		if !isMappedField(k, fieldMapping.FieldMappings) && v != "" {
			extensions[k] = sanitizeCEFValue(v)
		}
	}

	// Format extensions in order
	var parts []string

	// Ordered fields first
	for _, field := range fieldMapping.OrderedFields {
		if value, exists := extensions[field]; exists {
			parts = append(parts, fmt.Sprintf("%s=%s", field, value))
			delete(extensions, field)
		}
	}

	// Remaining fields alphabetically
	var remaining []string
	for k := range extensions {
		remaining = append(remaining, k)
	}
	sort.Strings(remaining)

	for _, field := range remaining {
		parts = append(parts, fmt.Sprintf("%s=%s", field, extensions[field]))
	}

	return header + strings.Join(parts, " ")
}

func sanitizeCEFValue(value string) string {
	// Escape special CEF characters
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "=", "\\=")
	value = strings.ReplaceAll(value, "|", "\\|")
	value = strings.ReplaceAll(value, "\n", "\\n")
	value = strings.ReplaceAll(value, "\r", "\\r")
	return value
}

func formatSyslogMessage(hostname, message string) string {
	priority := "134" // local0.info
	timestamp := time.Now().Format("Jan  2 15:04:05")
	return fmt.Sprintf("<%s>%s %s %s", priority, timestamp, hostname, message)
}

func getMapValue(m map[string]string, key, defaultVal string) string {
	if val, ok := m[key]; ok && val != "" {
		return val
	}
	return defaultVal
}

func isMappedField(fieldName string, fieldMappings map[string]string) bool {
	_, exists := fieldMappings[fieldName]
	return exists
}

func mapEventTypeToSeverity(eventType string) int {
	severityMap := map[string]int{
		"Threat":           10,
		"Malware":          10,
		"Attack":           9,
		"Intrusion":        9,
		"Security":         8,
		"Policy Violation": 7,
		"Warning":          6,
		"Alert":            6,
		"Connectivity":     5,
		"Network":          4,
		"Traffic":          3,
		"Info":             2,
		"Debug":            1,
	}

	if severity, exists := severityMap[eventType]; exists {
		return severity
	}
	return 5
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
		if _, err := fmt.Sscanf(value, "%d", &result); err == nil {
			return result
		}
	}
	return defaultValue
}

func getEnvOrBoolDefault(key string, defaultValue bool) bool {
	if value, exists := os.LookupEnv(key); exists {
		switch strings.ToLower(value) {
		case "true", "1", "yes", "y", "on":
			return true
		case "false", "0", "no", "n", "off":
			return false
		}
	}
	return defaultValue
}

func loadFieldMapping(filename string) FieldMapping {
	defaultMapping := FieldMapping{
		OrderedFields: []string{
			"rt", "src", "spt", "dst", "dpt", "proto", "act", "cs1", "cs2", "cs3", "cs4", "cs5", "cs6",
		},
		FieldMappings: map[string]string{
			"timestamp":      "rt",
			"src_ip":         "src",
			"src_port":       "spt",
			"dst_ip":         "dst",
			"dst_port":       "dpt",
			"protocol":       "proto",
			"action":         "act",
			"event_type":     "cs1",
			"event_sub_type": "cs2",
			"rule_name":      "cs3",
			"user":           "suser",
			"app_name":       "app",
			"category":       "cs4",
			"bytes_in":       "in",
			"bytes_out":      "out",
			"country_src":    "cs5",
			"country_dst":    "cs6",
		},
	}

	data, err := ioutil.ReadFile(filename)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("Creating default field mapping file: %s", filename)
			if jsonData, err := json.MarshalIndent(defaultMapping, "", "  "); err == nil {
				dir := filepath.Dir(filename)
				if err := os.MkdirAll(dir, 0755); err == nil {
					ioutil.WriteFile(filename, jsonData, 0644)
				}
			}
		}
		return defaultMapping
	}

	var mapping FieldMapping
	if err := json.Unmarshal(data, &mapping); err != nil {
		log.Printf("Error parsing field mapping file: %v, using defaults", err)
		return defaultMapping
	}

	return mapping
}

func loadMarkerFromFile(filename string) string {
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func saveMarkerToFile(filename string, marker string) error {
	if marker == "" {
		return nil // Don't save empty markers
	}
	dir := filepath.Dir(filename)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory for marker file: %w", err)
	}
	return ioutil.WriteFile(filename, []byte(marker), 0644)
}
