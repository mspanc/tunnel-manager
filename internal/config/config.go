package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"
	"tunnel/internal/slogf"
)

const (
	defaultServiceHostnamesAnnotation    = "cloudflare-tunnel-hostnames"
	defaultServiceUpstreamPortAnnotation = "cloudflare-tunnel-upstream-port"
	defaultSyncInterval                  = 15 * time.Second
	defaultLogLevel                      = slog.LevelInfo
)

type Config struct {
	CloudFlareAccountID           string
	CloudFlareTunnelID            string
	CloudFlareAPIToken            string
	ServiceHostnamesAnnotation    string
	ServiceUpstreamPortAnnotation string
	SyncInterval                  time.Duration
	LogLevel                      slog.Level
}

func LoadConfig() (*Config, error) {
	accountID := os.Getenv("CLOUDFLARE_ACCOUNT_ID")
	tunnelID := os.Getenv("CLOUDFLARE_TUNNEL_ID")
	apiToken := os.Getenv("CLOUDFLARE_API_TOKEN")

	if accountID == "" || tunnelID == "" || apiToken == "" {
		return nil, fmt.Errorf("CLOUDFLARE_ACCOUNT_ID, CLOUDFLARE_TUNNEL_ID and CLOUDFLARE_API_TOKEN must be set")
	}

	serviceHostnamesAnnotation := os.Getenv("SERVICE_HOSTNAMES_ANNOTATION")
	if serviceHostnamesAnnotation == "" {
		serviceHostnamesAnnotation = defaultServiceHostnamesAnnotation
	}

	serviceUpstreamPortAnnotation := os.Getenv("SERVICE_UPSTREAM_PORT_ANNOTATION")
	if serviceUpstreamPortAnnotation == "" {
		serviceUpstreamPortAnnotation = defaultServiceUpstreamPortAnnotation
	}

	logLevelEnv := os.Getenv("LOG_LEVEL")
	logLevel := defaultLogLevel
	switch logLevelEnv {
	case "debug":
		logLevel = slog.LevelDebug
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	case "":
		// use default
	default:
		return nil, fmt.Errorf("invalid LOG_LEVEL=%q", logLevelEnv)
	}

	syncInterval, err := parseSyncInterval()
	if err != nil {
		return nil, err
	}

	return &Config{
		CloudFlareAccountID:           accountID,
		CloudFlareTunnelID:            tunnelID,
		CloudFlareAPIToken:            apiToken,
		ServiceHostnamesAnnotation:    serviceHostnamesAnnotation,
		ServiceUpstreamPortAnnotation: serviceUpstreamPortAnnotation,
		SyncInterval:                  syncInterval,
		LogLevel:                      logLevel,
	}, nil
}

func (c *Config) Print(logger *slog.Logger) {
	logger.Info("Config:")
	slogf.Infof("- CloudFlare Account ID: %s\n", c.CloudFlareAccountID)
	slogf.Infof("- CloudFlare Tunnel ID: %s\n", c.CloudFlareTunnelID)
	slogf.Infof("- service domain label key: %s\n", c.ServiceHostnamesAnnotation)
	slogf.Infof("- service upstream port label key: %s\n", c.ServiceUpstreamPortAnnotation)
	slogf.Infof("- sync interval: %s\n", c.SyncInterval)
	slogf.Infof("- log level: %s\n", c.LogLevel.String())
}

func parseSyncInterval() (time.Duration, error) {
	raw := os.Getenv("SYNC_INTERVAL")
	if raw == "" {
		return defaultSyncInterval, nil
	}
	sec, err := strconv.Atoi(raw)
	if err != nil || sec <= 0 {
		return 0, fmt.Errorf("invalid SYNC_INTERVAL=%q", raw)
	}
	return time.Duration(sec) * time.Second, nil
}
