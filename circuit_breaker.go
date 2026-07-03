package main

import (
	"log"
	"sync"
	"time"
)

type CircuitBreakerState int

const (
	StateClosed CircuitBreakerState = iota
	StateOpen
	StateHalfOpen
)

type CircuitBreaker struct {
	state            CircuitBreakerState
	failureCount     int
	failureThreshold int
	recoveryTimeout  time.Duration
	lastFailureTime  time.Time
	mu               sync.RWMutex
	name             string
}

func NewCircuitBreaker(name string, threshold int, recoveryTimeout time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		state:            StateClosed,
		failureThreshold: threshold,
		recoveryTimeout:  recoveryTimeout,
		name:             name,
	}
}

func (cb *CircuitBreaker) Execute(fn func() ([]byte, error)) ([]byte, error) {
	cb.mu.RLock()
	currentState := cb.state
	cb.mu.RUnlock()

	switch currentState {
	case StateOpen:
		cb.mu.Lock()
		if time.Since(cb.lastFailureTime) >= cb.recoveryTimeout {
			log.Printf("Circuit breaker %s: open -> half-open", cb.name)
			cb.state = StateHalfOpen
			cb.mu.Unlock()
		} else {
			cb.mu.Unlock()
			log.Printf("Circuit breaker %s: open, request rejected", cb.name)
			return nil, ErrCircuitOpen
		}
	case StateHalfOpen:
		log.Printf("Circuit breaker %s: half-open, allowing test request", cb.name)
	}

	result, err := fn()
	if err != nil {
		cb.recordFailure()
	} else {
		cb.recordSuccess()
	}

	return result, err
}

func (cb *CircuitBreaker) recordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failureCount++
	cb.lastFailureTime = time.Now()

	log.Printf("Circuit breaker %s: failure %d/%d", cb.name, cb.failureCount, cb.failureThreshold)

	if cb.failureCount >= cb.failureThreshold {
		cb.state = StateOpen
		log.Printf("Circuit breaker %s: open (threshold reached)", cb.name)
	}
}

func (cb *CircuitBreaker) recordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failureCount = 0
	if cb.state == StateHalfOpen {
		log.Printf("Circuit breaker %s: half-open -> closed (recovered)", cb.name)
	}
	cb.state = StateClosed
}

func (cb *CircuitBreaker) IsOpen() bool {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.state == StateOpen
}

type CircuitOpenError struct{}

func (e *CircuitOpenError) Error() string {
	return "circuit breaker is open"
}

var ErrCircuitOpen = &CircuitOpenError{}
