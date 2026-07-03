package main

import (
	"log"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Gateway   GatewayConfig   `yaml:"gateway"`
	Cache     CacheConfig     `yaml:"cache"`
	Rate      RateConfig      `yaml:"rate_limiter"`
	Upstream  UpstreamConfig  `yaml:"upstream"`
	Dedup     DedupConfig     `yaml:"deduplication"`
	Telemetry TelemetryConfig `yaml:"telemetry"`
	Observer  ObserverConfig  `yaml:"observer"`
}

type GatewayConfig struct {
	Port     int    `yaml:"port"`
	LogLevel string `yaml:"log_level"`
}

type CacheConfig struct {
	RedisURL         string            `yaml:"redis_url"`
	Vector           VectorCacheConfig `yaml:"vector"`
	Jaccard          JaccardConfig     `yaml:"jaccard"`
	TTLHours         int               `yaml:"ttl_hours"`
	TemplateMatching TemplateConfig    `yaml:"template_matching"`
}

type VectorCacheConfig struct {
	Enabled             bool    `yaml:"enabled"`
	Dimension           int     `yaml:"dimension"`
	SimilarityThreshold float64 `yaml:"similarity_threshold"`
	MaxVectorsPerTenant int     `yaml:"max_vectors_per_tenant"`
	ExactHashFirst      bool    `yaml:"exact_hash_first"`
}

type JaccardConfig struct {
	Enabled   bool    `yaml:"enabled"`
	Threshold float64 `yaml:"threshold"`
}

type TemplateConfig struct {
	Enabled      bool `yaml:"enabled"`
	MaxTemplates int  `yaml:"max_templates"`
}

type RateConfig struct {
	Enabled       bool `yaml:"enabled"`
	MaxRequests   int  `yaml:"max_requests"`
	WindowMinutes int  `yaml:"window_minutes"`
}

type UpstreamConfig struct {
	Primary            UpstreamProviderConfig `yaml:"primary"`
	Fallback           UpstreamProviderConfig `yaml:"fallback"`
	Breaker            CircuitBreakerConfig   `yaml:"circuit_breaker"`
	AvailableProviders []string               `yaml:"available_providers"`
	ProviderURLs       map[string]string      `yaml:"provider_urls"`
}

type UpstreamProviderConfig struct {
	Provider       string `yaml:"provider"`
	BaseURL        string `yaml:"base_url"`
	TimeoutSeconds int    `yaml:"timeout_seconds"`
	MaxRetries     int    `yaml:"max_retries"`
}

type CircuitBreakerConfig struct {
	Enabled                bool `yaml:"enabled"`
	FailureThreshold       int  `yaml:"failure_threshold"`
	RecoveryTimeoutSeconds int  `yaml:"recovery_timeout_seconds"`
}

type DedupConfig struct {
	Enabled        bool `yaml:"enabled"`
	LockTTLSeconds int  `yaml:"lock_ttl_seconds"`
	MaxWaitSeconds int  `yaml:"max_wait_seconds"`
}

type TelemetryConfig struct {
	Enabled     bool `yaml:"enabled"`
	MetricsPort int  `yaml:"metrics_port"`
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("No config file at %s, using defaults: %v", path, err)
		return DefaultConfig(), nil
	}

	cfg := DefaultConfig()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		log.Printf("Failed to parse config, using defaults: %v", err)
		return DefaultConfig(), nil
	}

	log.Printf("Loaded configuration from %s", path)
	return cfg, nil
}

func DefaultConfig() *Config {
	return &Config{
		Gateway: GatewayConfig{
			Port:     8080,
			LogLevel: "info",
		},
		Cache: CacheConfig{
			RedisURL: "redis://localhost:6379",
			Vector: VectorCacheConfig{
				Enabled:             true,
				Dimension:           128,
				SimilarityThreshold: 0.75,
				MaxVectorsPerTenant: 10000,
				ExactHashFirst:      true,
			},
			Jaccard: JaccardConfig{
				Enabled:   true,
				Threshold: 0.60,
			},
			TTLHours: 24,
			TemplateMatching: TemplateConfig{
				Enabled:      true,
				MaxTemplates: 1000,
			},
		},
		Rate: RateConfig{
			Enabled:       true,
			MaxRequests:   60,
			WindowMinutes: 1,
		},
		Upstream: UpstreamConfig{
			Primary: UpstreamProviderConfig{
				Provider:       "groq",
				BaseURL:        "https://api.groq.com/openai/v1",
				TimeoutSeconds: 30,
				MaxRetries:     2,
			},
			Fallback: UpstreamProviderConfig{
				Provider:       "openai",
				BaseURL:        "https://api.openai.com/v1",
				TimeoutSeconds: 30,
				MaxRetries:     0,
			},
			AvailableProviders: []string{"groq", "openai", "mistral", "gemini", "anthropic", "cohere"},
			ProviderURLs: map[string]string{
				"groq":      "https://api.groq.com/openai/v1",
				"openai":    "https://api.openai.com/v1",
				"mistral":   "https://api.mistral.ai/v1/chat/completions",
				"gemini":    "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions",
				"anthropic": "https://api.anthropic.com/v1/messages",
				"cohere":    "https://api.cohere.ai/v1/chat",
			},
			Breaker: CircuitBreakerConfig{
				Enabled:                true,
				FailureThreshold:       5,
				RecoveryTimeoutSeconds: 30,
			},
		},
		Dedup: DedupConfig{
			Enabled:        true,
			LockTTLSeconds: 10,
			MaxWaitSeconds: 30,
		},
		Telemetry: TelemetryConfig{
			Enabled:     true,
			MetricsPort: 9090,
		},
		Observer: ObserverConfig{
			Enabled:            false,
			TrialDurationHours: 96,
			ContactSalesURL:    "",
		},
	}
}
