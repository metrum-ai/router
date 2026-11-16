// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const securityEventQueueSize = 1024

type securityEventStream struct {
	queue   chan securityAccessEvent
	done    chan struct{}
	output  io.WriteCloser
	client  *http.Client
	url     string
	secret  []byte
	dropped atomic.Int64
	failed  atomic.Int64
	closeMu sync.Once
	mu      sync.RWMutex
	closed  bool
}

type securityEventStreamOutput struct{ io.Writer }

func (securityEventStreamOutput) Close() error { return nil }

func newSecurityEventStream(cfg SecurityEventStreamConfig, getenv func(string) string) (*securityEventStream, error) {
	var output io.WriteCloser = securityEventStreamOutput{os.Stdout}
	if strings.EqualFold(strings.TrimSpace(cfg.Output), "file") {
		path := filepath.Clean(cfg.Path)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, fmt.Errorf("open security event output: %w", err)
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return nil, fmt.Errorf("open security event output: %w", err)
		}
		output = f
	}
	s := &securityEventStream{queue: make(chan securityAccessEvent, securityEventQueueSize), done: make(chan struct{}), output: output}
	if cfg.WebhookURL != "" {
		secret := getenv(cfg.WebhookSecretEnv)
		if len(secret) < 32 {
			_ = output.Close()
			return nil, fmt.Errorf("security event webhook secret must be at least 32 bytes")
		}
		s.url = cfg.WebhookURL
		s.secret = []byte(secret)
		s.client = &http.Client{Timeout: 2 * time.Second}
	}
	go s.run()
	return s, nil
}

func (s *securityEventStream) record(event securityAccessEvent) {
	if s == nil {
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		s.dropped.Add(1)
		return
	}
	select {
	case s.queue <- event:
	default:
		s.dropped.Add(1)
	}
}

func (s *securityEventStream) run() {
	defer close(s.done)
	for event := range s.queue {
		line, err := marshalOCSFSecurityEvent(event)
		if err != nil {
			s.failed.Add(1)
			continue
		}
		if _, err := s.output.Write(append(line, '\n')); err != nil {
			s.failed.Add(1)
		}
		if s.url != "" && s.deliver(line) != nil {
			s.failed.Add(1)
		}
	}
	_ = s.output.Close()
}

func (s *securityEventStream) deliver(line []byte) error {
	for attempt := 0; attempt < 2; attempt++ {
		stamp := time.Now().UTC().Format(time.RFC3339Nano)
		mac := hmac.New(sha256.New, s.secret)
		_, _ = mac.Write([]byte(stamp + "\n"))
		_, _ = mac.Write(line)
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, s.url, bytes.NewReader(line))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Router-Timestamp", stamp)
		req.Header.Set("X-Router-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		resp, err := s.client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
			err = fmt.Errorf("webhook returned status %d", resp.StatusCode)
		}
		if attempt == 0 {
			time.Sleep(50 * time.Millisecond)
		}
	}
	return fmt.Errorf("webhook delivery failed")
}

func (s *securityEventStream) close() {
	if s == nil {
		return
	}
	s.closeMu.Do(func() {
		s.mu.Lock()
		s.closed = true
		close(s.queue)
		s.mu.Unlock()
	})
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		log.Printf("security event stream shutdown exceeded its deadline")
	}
}

func (s *securityEventStream) prometheus() string {
	if s == nil {
		return ""
	}
	return fmt.Sprintf("# TYPE security_event_queue_depth gauge\nsecurity_event_queue_depth %d\n# TYPE security_event_dropped_total counter\nsecurity_event_dropped_total %d\n# TYPE security_event_delivery_failed_total counter\nsecurity_event_delivery_failed_total %d\n", len(s.queue), s.dropped.Load(), s.failed.Load())
}

func marshalOCSFSecurityEvent(event securityAccessEvent) ([]byte, error) {
	if event.TS.IsZero() {
		event.TS = time.Now().UTC()
	}
	classUID, categoryUID, className, activityID := ocsfSecurityClass(event.EventType)
	status, statusID := "Success", 1
	if event.StatusCode >= 400 {
		status, statusID = "Failure", 2
	}
	severityID := 1
	if classUID == 2004 || event.StatusCode == http.StatusUnauthorized || event.StatusCode == http.StatusForbidden || strings.Contains(strings.ToLower(event.EventType), "detector") {
		severityID = 3
	}
	uid := event.RequestID
	if uid == "" {
		var raw [16]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return nil, err
		}
		uid = hex.EncodeToString(raw[:])
	}
	obj := map[string]any{
		"activity_id":  activityID,
		"category_uid": categoryUID,
		"class_name":   className,
		"class_uid":    classUID,
		"metadata":     map[string]any{"version": "1.4.0", "product": map[string]any{"name": "Metrum AI Router", "vendor_name": "Metrum AI"}},
		"severity_id":  severityID,
		"status":       status,
		"status_id":    statusID,
		"time":         event.TS.UTC().UnixMilli(),
		"type_uid":     classUID*100 + activityID,
		"uid":          uid,
		"message":      safeOCSFMessage(event),
	}
	if classUID == 2004 {
		obj["status"] = "New"
		obj["status_id"] = 1
		obj["finding_info"] = map[string]any{"uid": uid, "title": safeOCSFValue(event.EventType), "product": map[string]any{"name": "Metrum AI Router"}}
	} else {
		obj["actor"] = map[string]any{"user": map[string]any{"name": "router"}}
		if classUID == 3002 {
			obj["user"] = map[string]any{"name": "router caller"}
			obj["service"] = map[string]any{"name": "Metrum AI Router"}
		} else {
			operation := safeOCSFValue(event.PathTemplate)
			if operation == "" {
				operation = safeOCSFValue(event.EventType)
			}
			obj["api"] = map[string]any{"operation": operation}
			obj["src_endpoint"] = map[string]any{"svc_name": "Metrum AI Router"}
			obj["http_request"] = map[string]any{"http_method": safeOCSFValue(event.HTTPMethod)}
			obj["http_response"] = map[string]any{"code": event.StatusCode}
		}
	}
	return json.Marshal(obj)
}

func ocsfSecurityClass(eventType string) (classUID, categoryUID int, name string, activityID int) {
	switch strings.ToLower(eventType) {
	case "api_auth_failed", "api_authz_failed", "admin_access", "admin_auth_failed", "admin_auth_throttled", "authentication", "auth_failure", "caller_suspended", "caller_throttled":
		activity := 1 // Logon
		if strings.Contains(strings.ToLower(eventType), "suspend") || strings.Contains(strings.ToLower(eventType), "thrott") {
			activity = 2 // Logoff
		}
		return 3002, 3, "Authentication", activity
	case "detector_signal", "dlp_hit", "firewall_block", "firewall_hold", "caller_quota_exhausted", "spend_breaker_trip", "provider_spend_breaker_trip", "provider_spend_breaker_reset", "honeytoken_use", "incident":
		return 2004, 2, "Detection Finding", 1
	default:
		activity := 2
		if eventType == "caller_released" || eventType == "containment" || strings.Contains(strings.ToLower(eventType), "contain") {
			activity = 3
		}
		return 6003, 6, "API Activity", activity
	}
}

func safeOCSFMessage(event securityAccessEvent) string {
	parts := []string{safeOCSFValue(event.EventType), safeOCSFValue(event.Outcome)}
	return strings.Trim(strings.Join(parts, " "), " ")
}

func safeOCSFValue(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 256 {
		value = value[:256]
	}
	return value
}
