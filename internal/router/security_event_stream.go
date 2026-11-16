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
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const securityEventQueueSize = 1024

type securityEventStream struct {
	queue          chan securityAccessEvent
	webhookQueue   chan []byte
	done           chan struct{}
	webhookDone    chan struct{}
	output         io.WriteCloser
	outputPath     string
	client         *http.Client
	url            string
	secret         []byte
	dropped        atomic.Int64
	webhookDropped atomic.Int64
	failed         atomic.Int64
	closeMu        sync.Once
	mu             sync.RWMutex
	closed         bool
}

type securityEventStreamOutput struct{ io.Writer }

func (securityEventStreamOutput) Close() error { return nil }

func newSecurityEventStream(cfg SecurityEventStreamConfig, getenv func(string) string) (*securityEventStream, error) {
	return newSecurityEventStreamWithClient(cfg, getenv, nil)
}

// The optional client is a test seam. Production always constructs the
// allow-listed egress client below.
func newSecurityEventStreamWithClient(cfg SecurityEventStreamConfig, getenv func(string) string, client *http.Client) (*securityEventStream, error) {
	var output io.WriteCloser = securityEventStreamOutput{os.Stdout}
	outputPath := ""
	if strings.EqualFold(strings.TrimSpace(cfg.Output), "file") {
		outputPath = filepath.Clean(cfg.Path)
		if err := os.MkdirAll(filepath.Dir(outputPath), 0700); err != nil {
			return nil, fmt.Errorf("open security event output: %w", err)
		}
		f, err := os.OpenFile(outputPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return nil, fmt.Errorf("open security event output: %w", err)
		}
		output = f
	}
	s := &securityEventStream{
		queue: make(chan securityAccessEvent, securityEventQueueSize), done: make(chan struct{}),
		output: output, outputPath: outputPath,
	}
	if cfg.WebhookURL != "" {
		secret := getenv(cfg.WebhookSecretEnv)
		if len(secret) < 32 {
			_ = output.Close()
			return nil, fmt.Errorf("security event webhook secret must be at least 32 bytes")
		}
		u, err := url.Parse(cfg.WebhookURL)
		var validationErr error
		if err == nil {
			if client == nil {
				validationErr = validateSecurityEventWebhookURL(u, cfg.WebhookAllowHosts)
			} else {
				validationErr = validateEgressURL(u, cfg.WebhookAllowHosts, false, "security event webhook")
			}
		}
		if err != nil || validationErr != nil {
			_ = output.Close()
			return nil, fmt.Errorf("security event webhook URL is not allowed")
		}
		s.url = u.String()
		s.secret = []byte(secret)
		if client == nil {
			client = newEgressHTTPClient(2*time.Second, cfg.WebhookAllowHosts, false, "security event webhook")
			client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		}
		s.client = client
		s.webhookQueue = make(chan []byte, securityEventQueueSize)
		s.webhookDone = make(chan struct{})
		go s.runWebhook()
	}
	go s.run()
	return s, nil
}

func validateSecurityEventWebhookURL(u *url.URL, allowHosts []string) error {
	if err := validateEgressURL(u, allowHosts, false, "security event webhook"); err != nil {
		return err
	}
	host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(u.Hostname())), ".")
	if egressHostIsTrustedLocal(host) {
		return fmt.Errorf("security event webhook host must not be local")
	}
	if ip := net.ParseIP(host); ip != nil && egressIPIsBlockedDestination(ip) {
		return fmt.Errorf("security event webhook destination must not be private")
	}
	return nil
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

// run owns the durable/stdout sink. It never waits for webhook I/O: enqueueing
// a serialized event onto webhookQueue is nonblocking and independently
// counted when the delivery worker falls behind.
func (s *securityEventStream) run() {
	defer close(s.done)
	for event := range s.queue {
		line, err := marshalOCSFSecurityEvent(event)
		if err != nil {
			s.failed.Add(1)
			continue
		}
		if err := s.reopenRotatedFile(); err != nil {
			s.failed.Add(1)
		} else if _, err := s.output.Write(append(line, '\n')); err != nil {
			s.failed.Add(1)
		}
		if s.webhookQueue != nil {
			select {
			case s.webhookQueue <- line:
			default:
				s.webhookDropped.Add(1)
			}
		}
	}
	if s.webhookQueue != nil {
		close(s.webhookQueue)
	}
	_ = s.output.Close()
}

func (s *securityEventStream) reopenRotatedFile() error {
	if s.outputPath == "" {
		return nil
	}
	current, ok := s.output.(*os.File)
	if !ok {
		return nil
	}
	pathInfo, pathErr := os.Stat(s.outputPath)
	fileInfo, fileErr := current.Stat()
	if pathErr == nil && fileErr == nil && os.SameFile(pathInfo, fileInfo) {
		return nil
	}
	next, err := os.OpenFile(s.outputPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("reopen rotated security event output: %w", err)
	}
	s.output = next
	_ = current.Close()
	return nil
}

func (s *securityEventStream) runWebhook() {
	defer close(s.webhookDone)
	for line := range s.webhookQueue {
		if s.deliver(line) != nil {
			s.failed.Add(1)
		}
	}
}

func (s *securityEventStream) deliver(line []byte) error {
	for attempt := 0; attempt < 2; attempt++ {
		stamp := time.Now().UTC().Format(time.RFC3339Nano)
		nonceBytes := make([]byte, 16)
		if _, err := rand.Read(nonceBytes); err != nil {
			return fmt.Errorf("create webhook nonce: %w", err)
		}
		nonce := hex.EncodeToString(nonceBytes)
		mac := hmac.New(sha256.New, s.secret)
		_, _ = mac.Write([]byte(stamp + "\n" + nonce + "\n"))
		_, _ = mac.Write(line)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(line))
		if err != nil {
			cancel()
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Router-Timestamp", stamp)
		req.Header.Set("X-Router-Nonce", nonce)
		req.Header.Set("X-Router-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		resp, err := s.client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
		}
		cancel()
		if err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		if err == nil {
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
	if s.webhookDone != nil {
		select {
		case <-s.webhookDone:
		case <-time.After(5 * time.Second):
			log.Printf("security event webhook shutdown exceeded its deadline")
		}
	}
}

func (s *securityEventStream) prometheus() string {
	if s == nil {
		return ""
	}
	return fmt.Sprintf("# TYPE security_event_queue_depth gauge\nsecurity_event_queue_depth %d\n# TYPE security_event_dropped_total counter\nsecurity_event_dropped_total %d\n# TYPE security_event_delivery_failed_total counter\nsecurity_event_delivery_failed_total %d\n# TYPE security_event_webhook_queue_depth gauge\nsecurity_event_webhook_queue_depth %d\n# TYPE security_event_webhook_dropped_total counter\nsecurity_event_webhook_dropped_total %d\n", len(s.queue), s.dropped.Load(), s.failed.Load(), len(s.webhookQueue), s.webhookDropped.Load())
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
	uid := safeOCSFValue(event.RequestID)
	if uid == "" {
		var raw [16]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return nil, err
		}
		uid = hex.EncodeToString(raw[:])
	}
	obj := map[string]any{
		"activity_id": activityID, "category_uid": categoryUID, "class_name": className, "class_uid": classUID,
		"metadata":    map[string]any{"version": "1.4.0", "product": map[string]any{"name": "Metrum AI Router", "vendor_name": "Metrum AI"}},
		"severity_id": severityID, "status": status, "status_id": statusID, "time": event.TS.UTC().UnixMilli(),
		"type_uid": classUID*100 + activityID, "uid": uid, "message": safeOCSFMessage(event),
	}
	if classUID == 2004 {
		obj["status"], obj["status_id"] = "New", 1
		title := safeOCSFValue(event.EventType)
		findingInfo := map[string]any{"uid": uid, "title": title, "product": map[string]any{"name": "Metrum AI Router"}}
		if event.EventType == "security_detector_signal" || event.EventType == "security_detector_failure" {
			if detectorID := safeOCSFValue(event.RequestedModel); detectorID != "" {
				findingInfo["title"] = detectorID
			}
			if reason := safeOCSFValue(event.ReasonCode); reason != "" {
				findingInfo["desc"] = reason
			}
		}
		obj["finding_info"] = findingInfo
		if event.Object != "" || event.OperationID != "" {
			obj["unmapped"] = map[string]any{"provider": safeOCSFValue(event.Object), "window": safeOCSFValue(event.OperationID), "action": safeOCSFValue(event.Action)}
		}
	} else if classUID == 3001 || classUID == 3002 {
		actorUser := map[string]any{"name": "Metrum AI Router"}
		if actorID := safeOCSFValue(event.AdminSubject); actorID != "" {
			actorUser["uid"] = actorID
		}
		obj["actor"] = map[string]any{"user": actorUser}
		user := map[string]any{}
		if callerID := safeOCSFValue(event.CallerID); callerID != "" {
			user["uid"] = callerID
		} else if adminSubject := safeOCSFValue(event.AdminSubject); adminSubject != "" {
			user["uid"] = adminSubject
		}
		obj["user"] = user
		obj["src_endpoint"] = ocsfEndpoint(event.IPAddress)
		obj["service"] = map[string]any{"name": "Metrum AI Router"}
		if event.OperationID != "" || event.ReasonCode != "" {
			obj["unmapped"] = map[string]any{"operation": safeOCSFValue(event.OperationID), "reason": safeOCSFValue(event.ReasonCode)}
		}
	} else {
		obj["actor"] = map[string]any{"user": map[string]any{"name": "Metrum AI Router"}}
		operation := safeOCSFValue(event.PathTemplate)
		if operation == "" {
			operation = safeOCSFValue(event.EventType)
		}
		obj["api"] = map[string]any{"operation": operation}
		obj["src_endpoint"] = ocsfEndpoint(event.IPAddress)
		obj["http_request"] = map[string]any{"http_method": safeOCSFValue(event.HTTPMethod)}
		obj["http_response"] = map[string]any{"code": event.StatusCode}
	}
	return json.Marshal(obj)
}

func ocsfEndpoint(ip string) map[string]any {
	endpoint := map[string]any{}
	if safeIP := safeOCSFValue(ip); safeIP != "" {
		endpoint["ip"] = safeIP
	}
	return endpoint
}

func ocsfSecurityClass(eventType string) (classUID, categoryUID int, name string, activityID int) {
	switch strings.ToLower(eventType) {
	case "api_auth_failed", "api_authz_failed", "admin_access", "admin_auth_failed", "admin_authz_failed", "admin_auth_throttled", "authentication", "auth_failure", "caller_throttled":
		return 3002, 3, "Authentication", 1
	case "caller_suspended":
		return 3001, 3, "Account Change", 5
	case "caller_released":
		return 3001, 3, "Account Change", 2
	case "detector_signal", "security_detector_signal", "security_detector_failure", "dlp_hit", "firewall_block", "firewall_hold", "caller_quota_exhausted", "spend_breaker_trip", "provider_spend_breaker_trip", "provider_spend_breaker_reset", "provider_usage_gap", "provider_usage_failure", "provider_usage_partial", "honeytoken_use", "incident":
		return 2004, 2, "Detection Finding", 1
	default:
		return 6003, 6, "API Activity", 1
	}
}

func isSecurityStreamEvent(eventType string) bool {
	switch strings.ToLower(strings.TrimSpace(eventType)) {
	case "api_auth_failed", "api_authz_failed", "admin_auth_failed", "admin_authz_failed", "admin_auth_throttled",
		"caller_suspended", "caller_released", "caller_throttled", "caller_quota_exhausted",
		"dlp_hit", "firewall_block", "firewall_hold", "security_detector_signal", "security_detector_failure", "authentication",
		"provider_spend_breaker_trip", "provider_spend_breaker_reset", "provider_usage_gap", "provider_usage_failure", "provider_usage_partial", "provider_usage_reconciled", "honeytoken_use", "incident":
		return true
	default:
		return false
	}
}

func sameSecurityEventConfig(a, b SecurityEventStreamConfig) bool {
	if a.Enabled != b.Enabled || a.Version != b.Version || a.Output != b.Output || a.Path != b.Path || a.WebhookURL != b.WebhookURL || a.WebhookSecretEnv != b.WebhookSecretEnv || len(a.WebhookAllowHosts) != len(b.WebhookAllowHosts) {
		return false
	}
	for i := range a.WebhookAllowHosts {
		if a.WebhookAllowHosts[i] != b.WebhookAllowHosts[i] {
			return false
		}
	}
	return true
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
