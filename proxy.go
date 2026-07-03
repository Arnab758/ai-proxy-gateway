package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"time"
)

type UpstreamProvider struct {
	config  UpstreamProviderConfig
	breaker *CircuitBreaker
	client  *http.Client
	apiKey  string // Per-request API key (overrides env var)
}

func NewUpstreamProvider(config UpstreamProviderConfig, breakerConfig CircuitBreakerConfig) *UpstreamProvider {
	var breaker *CircuitBreaker
	if breakerConfig.Enabled {
		breaker = NewCircuitBreaker(
			config.Provider,
			breakerConfig.FailureThreshold,
			time.Duration(breakerConfig.RecoveryTimeoutSeconds)*time.Second,
		)
	}

	// Connection pooling for low latency
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 20,
		IdleConnTimeout:     90 * time.Second,
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout: 5 * time.Second,
	}

	return &UpstreamProvider{
		config:  config,
		client:  &http.Client{Timeout: time.Duration(config.TimeoutSeconds) * time.Second, Transport: transport},
		breaker: breaker,
	}
}

func (p *UpstreamProvider) Call(originalBody []byte) []byte {
	if p.breaker != nil {
		result, err := p.breaker.Execute(func() ([]byte, error) {
			return p.doRequest(originalBody)
		})
		if err != nil {
			log.Printf("Circuit breaker blocked request to %s: %v", p.config.Provider, err)
			return []byte(`{"error": "Upstream provider unavailable (circuit open)"}`)
		}
		return result
	}

	result, _ := p.doRequestWithRetry(originalBody, p.config.MaxRetries)
	return result
}

func (p *UpstreamProvider) CallWithKey(originalBody []byte, apiKey string) []byte {
	// Save original key, use user's key for this request, then restore
	originalKey := p.apiKey
	p.apiKey = apiKey
	result := p.Call(originalBody)
	p.apiKey = originalKey
	return result
}

func (p *UpstreamProvider) doRequestWithRetry(originalBody []byte, retries int) ([]byte, error) {
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			log.Printf("Retry %d/%d for %s", attempt, retries, p.config.Provider)
			time.Sleep(time.Duration(attempt*500) * time.Millisecond)
		}
		result, err := p.doRequest(originalBody)
		if err == nil && result != nil && !bytes.Contains(result, []byte(`"error"`)) {
			return result, nil
		}
	}
	return p.doRequest(originalBody)
}

func (p *UpstreamProvider) doRequest(originalBody []byte) ([]byte, error) {
	endpoint := p.config.BaseURL + "/chat/completions"

	// Special handling for Anthropic (different API format)
	if p.config.Provider == "anthropic" {
		return p.doAnthropicRequest(originalBody)
	}

	req, err := http.NewRequest("POST", endpoint, bytes.NewBuffer(originalBody))
	if err != nil {
		log.Printf("Failed to create request for %s: %v", p.config.Provider, err)
		return []byte(`{"error": "Gateway routing error"}`), fmt.Errorf("request failed: %w", err)
	}

	// Use per-request API key if provided, otherwise fall back to env var
	apiKey := p.apiKey
	if apiKey == "" {
		apiKey = os.Getenv("UPSTREAM_API_KEY")
	}
	if apiKey == "" {
		log.Printf("No API key available for %s", p.config.Provider)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := p.client.Do(req)
	if err != nil {
		log.Printf("%s connection failed: %v", p.config.Provider, err)
		return nil, fmt.Errorf("%s failed: %w", p.config.Provider, err)
	}
	defer resp.Body.Close()

	responseBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Failed to read response from %s: %v", p.config.Provider, err)
		return nil, fmt.Errorf("%s read failed: %w", p.config.Provider, err)
	}

	return responseBytes, nil
}

// doAnthropicRequest handles Anthropic's different API format
func (p *UpstreamProvider) doAnthropicRequest(originalBody []byte) ([]byte, error) {
	endpoint := p.config.BaseURL + "/messages"

	req, err := http.NewRequest("POST", endpoint, bytes.NewBuffer(originalBody))
	if err != nil {
		return []byte(`{"error": "Gateway routing error"}`), fmt.Errorf("request failed: %w", err)
	}

	apiKey := p.apiKey
	if apiKey == "" {
		apiKey = os.Getenv("UPSTREAM_API_KEY")
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic failed: %w", err)
	}
	defer resp.Body.Close()

	responseBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("anthropic read failed: %w", err)
	}

	return responseBytes, nil
}

var (
	primaryProvider  *UpstreamProvider
	fallbackProvider *UpstreamProvider
)

func InitUpstreamProviders(cfg *Config) {
	primaryProvider = NewUpstreamProvider(cfg.Upstream.Primary, cfg.Upstream.Breaker)

	if cfg.Upstream.Fallback.Provider != "" && cfg.Upstream.Fallback.Provider != cfg.Upstream.Primary.Provider {
		fallbackProvider = NewUpstreamProvider(cfg.Upstream.Fallback, CircuitBreakerConfig{
			Enabled: false,
		})
		log.Printf("Fallback provider configured: %s", cfg.Upstream.Fallback.Provider)
	}
}

func SetProviderAPIKey(provider *UpstreamProvider, key string) {
	if provider != nil {
		provider.apiKey = key
	}
}

func buildProxyResponse(originalBody []byte, userAPIKey string) []byte {
	log.Println("Cache miss. Calling upstream provider...")

	// Use user's API key if provided, otherwise use server's key
	if userAPIKey != "" {
		result := primaryProvider.CallWithKey(originalBody, userAPIKey)
		if result != nil && !bytes.Contains(result, []byte(`"error"`)) {
			return result
		}

		if fallbackProvider != nil {
			log.Println("Primary failed. Trying fallback provider with user's key...")
			result = fallbackProvider.CallWithKey(originalBody, userAPIKey)
			if result != nil && !bytes.Contains(result, []byte(`"error"`)) {
				return result
			}
		}
	} else {
		// No user key provided, use server's configured key
		result := primaryProvider.Call(originalBody)
		if result != nil && !bytes.Contains(result, []byte(`"error"`)) {
			return result
		}

		if fallbackProvider != nil {
			log.Println("Primary failed. Trying fallback provider...")
			result = fallbackProvider.Call(originalBody)
			if result != nil && !bytes.Contains(result, []byte(`"error"`)) {
				return result
			}
		}
	}

	return []byte(`{"error": "All upstream providers unavailable"}`)
}
