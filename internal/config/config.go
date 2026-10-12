package config

import (
	"bufio"
	"bytes"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all runtime configuration, sourced from environment variables.
type Config struct {
	DatabaseURL    string
	RedisURL       string
	Port           int
	CacheTTL       time.Duration
	PluginDir      string
	JWTSecret      string
	AuthUser       string        // Username for /api/v1/_auth/login
	AuthPass       string        // Password for /api/v1/_auth/login
	AuthTokenTTL   time.Duration // Expiration duration for generated JWTs
	PGHookURL      string        // External webhook URL called on PG events
	EnableUI       bool          // Enables web UI dashboard at / and /ui
	EnableSQL      bool          // Enables executing SQL queries in UI mode
	EnableDataView bool          // Enables viewing table data with dynamic filters and pagination in UI mode
	DataLimit      int           // Max / default row limit for view data queries
	CDCEnabled     bool          // Enables PostgreSQL logical replication CDC engine
	CDCSlot        string        // Replication slot name (default: dynago_cdc_slot)
	CDCPlugin       string        // Replication plugin (default: test_decoding)
	CDCPollInterval time.Duration // Interval between polling CDC slot changes
	PublicFunctions []string      // Function names accessible without authentication
	PublicTables    []string      // Table names accessible without authentication
}

// Load reads configuration from environment variables with sensible defaults.
func Load() *Config {
	loadDotEnv(".env")
	port, _ := strconv.Atoi(getEnv("DYNAGO_PORT", "8080"))
	ttl, _ := time.ParseDuration(getEnv("DYNAGO_CACHE_TTL", "5m"))
	authTTL, _ := time.ParseDuration(getEnv("DYNAGO_AUTH_TOKEN_TTL", "24h"))
	if authTTL <= 0 {
		authTTL = 24 * time.Hour
	}

	dataLimit, _ := strconv.Atoi(getEnv("DYNAGO_DATA_LIMIT", "50"))
	if dataLimit <= 0 {
		dataLimit = 50
	}

	cdcPollInterval, _ := time.ParseDuration(getEnv("DYNAGO_CDC_POLL_INTERVAL", "250ms"))
	if cdcPollInterval <= 0 {
		cdcPollInterval = 250 * time.Millisecond
	}

	publicFuncsStr := getEnv("DYNAGO_PUBLIC_FUNCTIONS", "currency_convert")
	var publicFuncs []string
	for _, fn := range strings.Split(publicFuncsStr, ",") {
		fn = strings.TrimSpace(fn)
		if fn != "" {
			publicFuncs = append(publicFuncs, fn)
		}
	}

	publicTablesStr := getEnv("DYNAGO_PUBLIC_TABLES", "")
	var publicTables []string
	for _, t := range strings.Split(publicTablesStr, ",") {
		t = strings.TrimSpace(t)
		if t != "" {
			publicTables = append(publicTables, t)
		}
	}

	return &Config{
		DatabaseURL:     getEnv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable"),
		RedisURL:        getEnv("REDIS_URL", ""),
		Port:            port,
		CacheTTL:        ttl,
		PluginDir:       getEnv("DYNAGO_PLUGIN_DIR", "./plugins"),
		JWTSecret:       getEnv("DYNAGO_JWT_SECRET", ""),
		AuthUser:        getEnv("DYNAGO_AUTH_USER", "admin"),
		AuthPass:        getEnv("DYNAGO_AUTH_PASS", "dynago123"),
		AuthTokenTTL:    authTTL,
		PGHookURL:       getEnv("DYNAGO_PG_HOOK_URL", ""),
		EnableUI:        getEnvBool("DYNAGO_ENABLE_UI", false),
		EnableSQL:       getEnvBool("DYNAGO_ENABLE_SQL", false),
		EnableDataView:  getEnvBool("DYNAGO_ENABLE_DATA_VIEW", false),
		DataLimit:       dataLimit,
		CDCEnabled:      getEnvBool("DYNAGO_CDC_ENABLED", true),
		CDCSlot:         getEnv("DYNAGO_CDC_SLOT", "dynago_cdc_slot"),
		CDCPlugin:       getEnv("DYNAGO_CDC_PLUGIN", "test_decoding"),
		CDCPollInterval: cdcPollInterval,
		PublicFunctions: publicFuncs,
		PublicTables:    publicTables,
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}

// loadDotEnv loads KEY=VALUE pairs from a file into process environment if not already set.
func loadDotEnv(filepath string) {
	data, err := os.ReadFile(filepath)
	if err != nil {
		return
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			k := strings.TrimSpace(parts[0])
			v := strings.TrimSpace(parts[1])
			if _, exists := os.LookupEnv(k); !exists {
				os.Setenv(k, v)
			}
		}
	}
}
