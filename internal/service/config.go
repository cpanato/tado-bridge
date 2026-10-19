package service

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"
)

type Config struct {
	ListenAddr     string
	PollInterval   time.Duration
	MaxSnapshotAge time.Duration
	DailyBudget    int
	APIToken       string
	MetricsEnabled bool
}

// LoadConfig accepts literal bind addresses, resolving localhost to loopback
// without DNS. Non-loopback binds require explicit opt-in and authentication.
func LoadConfig(getenv func(string) string) (Config, error) {
	config := Config{ListenAddr: "127.0.0.1:8080", PollInterval: 30 * time.Minute, MaxSnapshotAge: 2 * time.Hour, DailyBudget: 90, APIToken: getenv("API_TOKEN")}
	if value := getenv("LISTEN_ADDR"); value != "" {
		config.ListenAddr = value
	}
	for _, item := range []struct {
		name   string
		target *time.Duration
	}{{"POLL_INTERVAL", &config.PollInterval}, {"MAX_SNAPSHOT_AGE", &config.MaxSnapshotAge}} {
		if value := getenv(item.name); value != "" {
			parsed, err := time.ParseDuration(value)
			if err != nil || parsed <= 0 {
				return Config{}, fmt.Errorf("%s must be a positive duration", item.name)
			}
			*item.target = parsed
		}
	}
	if config.PollInterval < 5*time.Minute {
		return Config{}, errors.New("POLL_INTERVAL must be at least 5m")
	}
	if value := getenv("TADO_DAILY_REQUEST_BUDGET"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 1000000 {
			return Config{}, errors.New("TADO_DAILY_REQUEST_BUDGET must be between 1 and 1000000")
		}
		config.DailyBudget = parsed
	}
	for _, char := range config.APIToken {
		if char <= ' ' || char >= 127 {
			return Config{}, errors.New("API_TOKEN must contain only printable ASCII without whitespace")
		}
	}
	allow, err := boolean(getenv, "ALLOW_NETWORK_EXPOSURE")
	if err != nil {
		return Config{}, err
	}
	config.MetricsEnabled, err = boolean(getenv, "METRICS_ENABLED")
	if err != nil {
		return Config{}, err
	}
	host, port, err := net.SplitHostPort(config.ListenAddr)
	if err != nil {
		return Config{}, errors.New("LISTEN_ADDR must be a host:port address")
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 0 || number > 65535 {
		return Config{}, errors.New("LISTEN_ADDR requires a numeric port between 0 and 65535")
	}
	if host == "localhost" {
		host = "127.0.0.1"
	}
	ip := net.ParseIP(host)
	if host != "" && ip == nil {
		return Config{}, errors.New("LISTEN_ADDR host must be localhost or a literal IP address")
	}
	if (ip == nil || !ip.IsLoopback()) && (!allow || config.APIToken == "") {
		return Config{}, errors.New("non-loopback binding requires ALLOW_NETWORK_EXPOSURE=true and API_TOKEN")
	}
	config.ListenAddr = net.JoinHostPort(host, port)
	return config, nil
}

func boolean(getenv func(string) string, name string) (bool, error) {
	value := getenv(name)
	if value == "" {
		return false, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean", name)
	}
	return parsed, nil
}
