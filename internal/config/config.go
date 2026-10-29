// Package config loads runtime configuration from environment variables and flags.
package config

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Listen         string
	DataDir        string
	TLSCert        string
	TLSKey         string
	SecretKey      []byte
	SecureCookies  bool
	AllowedOrigins []string
	ResyncInterval time.Duration
	BackupMaxAge   time.Duration
	DevMode        bool
	MetricsToken   string
	DiskFreeMin    float64
	BatteryMin     float64

	AIProvider   string // anthropic | ollama | "" (disabled)
	AIModel      string
	AIEffort     string
	AIFallbacks  bool
	AIChatLimit  int
	AIAutoReview bool
	OllamaURL    string
	OllamaModel  string
	OllamaNumCtx int
}

// Load parses flags from args, falling back to HAFLEET_* environment variables.
func Load(args []string) (*Config, error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	c := &Config{}
	var secretKey, secretKeyFile, origins string
	fs.StringVar(&c.Listen, "listen", env("HAFLEET_LISTEN", ":8080"), "listen address")
	fs.StringVar(&c.DataDir, "data-dir", env("HAFLEET_DATA_DIR", "./data"), "directory for the SQLite database")
	fs.StringVar(&c.TLSCert, "tls-cert", env("HAFLEET_TLS_CERT", ""), "TLS certificate file (optional)")
	fs.StringVar(&c.TLSKey, "tls-key", env("HAFLEET_TLS_KEY", ""), "TLS key file (optional)")
	fs.StringVar(&secretKey, "secret-key", env("HAFLEET_SECRET_KEY", ""), "32-byte key (hex or base64) used to encrypt HA tokens")
	fs.StringVar(&secretKeyFile, "secret-key-file", env("HAFLEET_SECRET_KEY_FILE", ""), "file containing the secret key")
	fs.StringVar(&origins, "allowed-origins", env("HAFLEET_ALLOWED_ORIGINS", ""), "comma-separated extra allowed Origin values")
	fs.BoolVar(&c.SecureCookies, "secure-cookies", env("HAFLEET_SECURE_COOKIES", "true") == "true", "set Secure flag on cookies (disable only for plain-http local dev)")
	fs.DurationVar(&c.ResyncInterval, "resync-interval", envDuration("HAFLEET_RESYNC_INTERVAL", 10*time.Minute), "full resync interval per instance")
	fs.DurationVar(&c.BackupMaxAge, "backup-max-age", envDuration("HAFLEET_BACKUP_MAX_AGE", 7*24*time.Hour), "age after which a backup is considered stale")
	fs.Float64Var(&c.DiskFreeMin, "disk-free-min", envFloat("HAFLEET_DISK_FREE_MIN", 15), "warn when less than this percentage of disk space is free")
	fs.Float64Var(&c.BatteryMin, "battery-min", envFloat("HAFLEET_BATTERY_MIN", 15), "report battery sensors below this percentage")
	fs.StringVar(&c.MetricsToken, "metrics-token", env("HAFLEET_METRICS_TOKEN", ""), "bearer token for /metrics (Prometheus); empty disables the endpoint")
	fs.BoolVar(&c.DevMode, "dev", env("HAFLEET_DEV", "") == "true", "development mode (relaxed CSP, frontend served by Vite)")
	fs.StringVar(&c.AIProvider, "ai-provider", env("HAFLEET_AI_PROVIDER", ""), "AI provider: anthropic, ollama, or empty to disable AI features")
	fs.StringVar(&c.AIModel, "ai-model", env("HAFLEET_AI_MODEL", ""), "model override for the anthropic provider (default claude-opus-5-5)")
	fs.StringVar(&c.AIEffort, "ai-effort", env("HAFLEET_AI_EFFORT", "medium"), "effort level for Claude: low, medium, high")
	fs.BoolVar(&c.AIFallbacks, "ai-fallbacks", env("HAFLEET_AI_FALLBACKS", "true") == "true", "enable Claude server-side refusal fallbacks")
	fs.IntVar(&c.AIChatLimit, "ai-limit", envInt("HAFLEET_AI_CHAT_LIMIT", 60), "AI requests per user per hour")
	fs.BoolVar(&c.AIAutoReview, "ai-auto-review", env("HAFLEET_AI_AUTO_REVIEW", "") == "true", "review new updates in the background")
	fs.StringVar(&c.OllamaURL, "ollama-url", env("HAFLEET_OLLAMA_URL", "http://localhost:11434"), "Ollama base URL")
	fs.StringVar(&c.OllamaModel, "ollama-model", env("HAFLEET_OLLAMA_MODEL", ""), "Ollama model name (required for the ollama provider)")
	fs.IntVar(&c.OllamaNumCtx, "ollama-num-ctx", envInt("HAFLEET_OLLAMA_NUM_CTX", 32768), "context window for the Ollama model")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if origins != "" {
		for _, o := range strings.Split(origins, ",") {
			if o = strings.TrimSpace(o); o != "" {
				c.AllowedOrigins = append(c.AllowedOrigins, o)
			}
		}
	}
	switch c.AIProvider {
	case "", "anthropic", "ollama":
	default:
		return nil, fmt.Errorf("unknown -ai-provider %q (use anthropic or ollama)", c.AIProvider)
	}
	switch c.AIEffort {
	case "low", "medium", "high", "xhigh", "max":
	default:
		return nil, fmt.Errorf("invalid -ai-effort %q", c.AIEffort)
	}
	if c.DiskFreeMin <= 0 || c.DiskFreeMin >= 100 || c.BatteryMin <= 0 || c.BatteryMin >= 100 {
		return nil, errors.New("-disk-free-min and -battery-min must be between 0 and 100")
	}
	if c.MetricsToken != "" && len(c.MetricsToken) < 16 {
		return nil, errors.New("-metrics-token must be at least 16 characters")
	}
	if (c.TLSCert == "") != (c.TLSKey == "") {
		return nil, errors.New("both -tls-cert and -tls-key must be set")
	}

	if secretKey == "" && secretKeyFile != "" {
		b, err := os.ReadFile(secretKeyFile)
		if err != nil {
			return nil, fmt.Errorf("read secret key file: %w", err)
		}
		secretKey = strings.TrimSpace(string(b))
	}
	if secretKey == "" {
		return nil, errors.New("a secret key is required: set HAFLEET_SECRET_KEY or HAFLEET_SECRET_KEY_FILE (generate one with `hafleet genkey`)")
	}
	key, err := DecodeKey(secretKey)
	if err != nil {
		return nil, err
	}
	c.SecretKey = key
	return c, nil
}

// DecodeKey accepts a 32-byte key encoded as hex or base64.
func DecodeKey(s string) ([]byte, error) {
	if b, err := hex.DecodeString(s); err == nil && len(b) == 32 {
		return b, nil
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil && len(b) == 32 {
		return b, nil
	}
	return nil, errors.New("secret key must be 32 bytes, hex or base64 encoded")
}

func env(k, def string) string {
	if v, ok := os.LookupEnv(k); ok {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v, ok := os.LookupEnv(k); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envFloat(k string, def float64) float64 {
	if v, ok := os.LookupEnv(k); ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func envDuration(k string, def time.Duration) time.Duration {
	if v, ok := os.LookupEnv(k); ok {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
