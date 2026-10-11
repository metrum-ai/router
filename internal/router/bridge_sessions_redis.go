// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto/sha256"
	"crypto/sha3"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// bridgeRedisLegacyKeyGrace extends the legacy-key window past the session
// TTL so sessions written by not-yet-upgraded replicas during a rolling
// upgrade stay readable.
const bridgeRedisLegacyKeyGrace = time.Hour

type redisBridgeSessionStore struct {
	client    *redis.Client
	namespace string
	// legacyKeysUntil bounds the upgrade window. Until then the store also
	// reads, writes and deletes the pre-SHA3 key layout so in-flight bridge
	// sessions and mixed-version replicas keep working. Zero disables it.
	legacyKeysUntil time.Time
	now             func() time.Time
}

// extendLegacyKeyWindow keeps legacy-key compatibility for at least one
// session TTL (plus grace) from now.
func (s *redisBridgeSessionStore) extendLegacyKeyWindow(ttl time.Duration) {
	if s == nil {
		return
	}
	until := s.clock().Add(ttl + bridgeRedisLegacyKeyGrace)
	if until.After(s.legacyKeysUntil) {
		s.legacyKeysUntil = until
	}
}

func (s *redisBridgeSessionStore) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *redisBridgeSessionStore) legacyKeysActive() bool {
	return !s.legacyKeysUntil.IsZero() && s.clock().Before(s.legacyKeysUntil)
}

func newRedisBridgeSessionStore(ctx context.Context, cfg BridgeStatefulSessionsRedisConfig) (*redisBridgeSessionStore, error) {
	cfg = bridgeStatefulSessionRedisConfig(cfg)
	username, err := envValueIfConfigured(cfg.UsernameEnv, "username_env")
	if err != nil {
		return nil, err
	}
	password, err := envValueIfConfigured(cfg.PasswordEnv, "password_env")
	if err != nil {
		return nil, err
	}
	options := &redis.Options{
		Addr:         strings.TrimSpace(cfg.Address),
		Username:     username,
		Password:     password,
		DB:           cfg.DB,
		DialTimeout:  redisTimeout(cfg.ConnectTimeoutMS),
		ReadTimeout:  redisTimeout(cfg.ReadTimeoutMS),
		WriteTimeout: redisTimeout(cfg.WriteTimeoutMS),
	}
	if cfg.PoolSize > 0 {
		options.PoolSize = cfg.PoolSize
	}
	if cfg.TLS.Enabled {
		options.TLSConfig = &tls.Config{
			ServerName:         strings.TrimSpace(cfg.TLS.ServerName),
			InsecureSkipVerify: cfg.TLS.InsecureSkipVerify,
			MinVersion:         tls.VersionTLS12,
		}
	}
	client := redis.NewClient(options)
	pingCtx := ctx
	cancel := func() {}
	if cfg.ConnectTimeoutMS > 0 {
		pingCtx, cancel = context.WithTimeout(ctx, redisTimeout(cfg.ConnectTimeoutMS))
	}
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ping redis session backend: %w", err)
	}
	return &redisBridgeSessionStore{client: client, namespace: strings.TrimSpace(cfg.Namespace)}, nil
}

func (s *redisBridgeSessionStore) Get(ctx context.Context, key string) (bridgeSessionEntry, bool, error) {
	if s == nil || s.client == nil || strings.TrimSpace(key) == "" {
		return bridgeSessionEntry{}, false, nil
	}
	value, err := s.client.Get(ctx, s.key(key)).Result()
	if err == redis.Nil && s.legacyKeysActive() {
		value, err = s.client.Get(ctx, bridgeRedisLegacySessionKey(s.namespace, key)).Result()
	}
	if err == redis.Nil {
		return bridgeSessionEntry{}, false, nil
	}
	if err != nil {
		return bridgeSessionEntry{}, false, err
	}
	if strings.TrimSpace(value) == "" {
		return bridgeSessionEntry{}, false, nil
	}
	return bridgeSessionEntry{PreviousResponseID: value}, true, nil
}

func (s *redisBridgeSessionStore) Set(ctx context.Context, key, previousResponseID string, ttl time.Duration, maxEntries int) error {
	if s == nil || s.client == nil || strings.TrimSpace(key) == "" || strings.TrimSpace(previousResponseID) == "" {
		return nil
	}
	if !s.legacyKeysActive() {
		return s.client.Set(ctx, s.key(key), previousResponseID, ttl).Err()
	}
	pipe := s.client.TxPipeline()
	pipe.Set(ctx, s.key(key), previousResponseID, ttl)
	pipe.Set(ctx, bridgeRedisLegacySessionKey(s.namespace, key), previousResponseID, ttl)
	_, err := pipe.Exec(ctx)
	return err
}

func (s *redisBridgeSessionStore) Delete(ctx context.Context, key string) error {
	if s == nil || s.client == nil || strings.TrimSpace(key) == "" {
		return nil
	}
	if s.legacyKeysActive() {
		return s.client.Del(ctx, s.key(key), bridgeRedisLegacySessionKey(s.namespace, key)).Err()
	}
	return s.client.Del(ctx, s.key(key)).Err()
}

func (s *redisBridgeSessionStore) Close() error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.client.Close()
}

func (s *redisBridgeSessionStore) key(key string) string {
	return bridgeRedisSessionKey(s.namespace, key)
}

// bridgeRedisBackendKey fingerprints a Redis backend configuration so model
// groups that share one backend share one client. It covers only connection
// settings and the *names* of the credential environment variables; it never
// reads environment values, and the inline Username/Password fields are
// deliberately excluded. The fingerprint is an in-process map key only.
func bridgeRedisBackendKey(cfg BridgeStatefulSessionsRedisConfig) string {
	cfg = bridgeStatefulSessionRedisConfig(cfg)
	parts := []string{
		strings.TrimSpace(cfg.Address),
		strings.TrimSpace(cfg.Namespace),
		fmt.Sprintf("%d", cfg.DB),
		strings.TrimSpace(cfg.UsernameEnv),
		strings.TrimSpace(cfg.PasswordEnv),
		fmt.Sprintf("%t", cfg.TLS.Enabled),
		strings.TrimSpace(cfg.TLS.ServerName),
		fmt.Sprintf("%t", cfg.TLS.InsecureSkipVerify),
		fmt.Sprintf("%d", cfg.ConnectTimeoutMS),
		fmt.Sprintf("%d", cfg.ReadTimeoutMS),
		fmt.Sprintf("%d", cfg.WriteTimeoutMS),
		fmt.Sprintf("%d", cfg.PoolSize),
	}
	sum := sha3.Sum512([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:32])
}

// bridgeRedisSessionKey is the Redis key for a bridge session. The
// "bridge-session-v2" segment separates the SHA3-512 layout from the legacy
// SHA-256 one so the two can never collide.
func bridgeRedisSessionKey(namespace, hashedSessionKey string) string {
	sum := sha3.Sum512([]byte(strings.TrimSpace(hashedSessionKey)))
	return "metrum-ai-router:bridge-session-v2:" + bridgeRedisNamespace(namespace) + ":" + hex.EncodeToString(sum[:])
}

// bridgeRedisLegacySessionKey is the pre-SHA3 key layout. It is read and
// written only inside the bounded upgrade window (see legacyKeysUntil) and
// can be removed in a later release. Its input is the already-hashed,
// non-secret session lookup key.
func bridgeRedisLegacySessionKey(namespace, hashedSessionKey string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(hashedSessionKey)))
	return "metrum-ai-router:bridge-session:" + bridgeRedisNamespace(namespace) + ":" + hex.EncodeToString(sum[:])
}

func bridgeRedisNamespace(namespace string) string {
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		namespace = defaultBridgeStatefulSessionRedisNamespace
	}
	return namespace
}

func envValueIfConfigured(envName, field string) (string, error) {
	envName = strings.TrimSpace(envName)
	if envName == "" {
		return "", nil
	}
	value := os.Getenv(envName)
	if value == "" {
		return "", fmt.Errorf("redis session backend %s %s is set but the environment variable is empty or unset", field, envName)
	}
	return value, nil
}

func redisTimeout(ms int) time.Duration {
	if ms <= 0 {
		return 500 * time.Millisecond
	}
	return time.Duration(ms) * time.Millisecond
}
