// tadofake is a snapshot-only fixture tool, never a production service.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/cpanato/tado-bridge/internal/credentials"
	"github.com/cpanato/tado-bridge/internal/domain"
	"github.com/cpanato/tado-bridge/internal/testutil/tadofake"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) < 2 {
		return errors.New("usage: tadofake prepare DIR | serve | check")
	}
	switch os.Args[1] {
	case "prepare":
		if len(os.Args) != 3 {
			return errors.New("prepare requires an output directory")
		}
		return prepare(os.Args[2])
	case "serve":
		return serve()
	case "preview":
		if len(os.Args) != 2 {
			return errors.New("preview accepts no arguments")
		}
		return preview()
	case "check":
		expectedOAuth := 1
		if len(os.Args) == 3 && os.Args[2] == "--reused-token" {
			expectedOAuth = 0
		} else if len(os.Args) != 2 {
			return errors.New("check accepts only --reused-token")
		}
		return check(expectedOAuth)
	default:
		return errors.New("unknown fixture command")
	}
}
func prepare(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	now := time.Now()
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "tado-bridge synthetic test CA"}, DNSNames: []string{"my.tado.com", "login.tado.com", "hops.tado.com"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		return err
	}
	encoded, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "tls.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "tls.key"), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: encoded}), 0o600)
}
func serve() error {
	state := "/state/credentials.json"
	if _, err := os.Stat(state); os.IsNotExist(err) {
		if err := credentials.Save(state, credentials.Token{AccessToken: "expired-fixture-access", RefreshToken: tadofake.InitialRefreshToken, ExpiresAt: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)}); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	server := &http.Server{Addr: ":8443", Handler: &tadofake.Server{}, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-ctx.Done()
		shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = server.Shutdown(shutdown)
	}()
	err := server.ListenAndServeTLS("/tls/tls.crt", "/tls/tls.key")
	cancel()
	<-stopped
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
func check(expectedOAuth int) error {
	address := os.Getenv("BRIDGE_URL")
	if !strings.HasPrefix(address, "http://") {
		return errors.New("BRIDGE_URL must identify the in-cluster fixture HTTP service")
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	request := func(path, token string, status int) ([]byte, error) {
		req, err := http.NewRequest(http.MethodGet, address+path, nil)
		if err != nil {
			return nil, err
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := client.Do(req)
		if err != nil {
			return nil, errors.New("fixture bridge request failed")
		}
		defer func() { _ = response.Body.Close() }()
		data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if err != nil {
			return nil, err
		}
		if response.StatusCode != status {
			return nil, fmt.Errorf("fixture endpoint %s returned %d, expected %d", path, response.StatusCode, status)
		}
		return data, nil
	}
	for _, path := range []string{"/healthz", "/readyz"} {
		if _, err := request(path, "", 200); err != nil {
			return err
		}
	}
	if _, err := request("/api/v1/snapshot", "", 401); err != nil {
		return err
	}
	if _, err := request("/api/v1/snapshot", "incorrect-fixture-token", 401); err != nil {
		return err
	}
	data, err := request("/api/v1/snapshot", os.Getenv("API_TOKEN"), 200)
	if err != nil {
		return err
	}
	var snapshot domain.Snapshot
	if json.Unmarshal(data, &snapshot) != nil || len(snapshot.Homes) != 2 || len(snapshot.Homes[0].Zones) != 1 || len(snapshot.Homes[1].Zones) != 8 {
		return errors.New("unexpected fixture homes/rooms")
	}
	zone := snapshot.Homes[0].Zones[0]
	if zone.TemperatureC == nil || *zone.TemperatureC != 0 || zone.TargetTemperatureC != nil || zone.HumidityPercent == nil || *zone.HumidityPercent != 0 || zone.HeatingActive == nil || *zone.HeatingActive {
		return errors.New("fixture null/zero normalization failed")
	}
	again, err := request("/api/v1/snapshot", os.Getenv("API_TOKEN"), 200)
	if err != nil {
		return err
	}
	if string(again) != string(data) {
		return errors.New("consumer triggered a new collection")
	}
	metrics, err := request("/metrics", os.Getenv("API_TOKEN"), 200)
	if err != nil {
		return err
	}
	if err := checkOAuthAttempts(metrics, expectedOAuth); err != nil {
		return err
	}
	token, err := credentials.Load("/state/credentials.json")
	if err != nil {
		return err
	}
	if token.RefreshToken != tadofake.RotatedRefreshToken {
		return errors.New("rotated fixture token was not persisted to PVC")
	}
	fmt.Println("Fixture deployment: readiness, V3/X collection, authentication, cache, metrics and PVC token rotation passed.")
	return nil
}

func checkOAuthAttempts(metrics []byte, expected int) error {
	for line := range strings.SplitSeq(string(metrics), "\n") {
		value, ok := strings.CutPrefix(line, `tado_bridge_upstream_requests_total{kind="oauth"} `)
		if !ok {
			continue
		}
		attempts, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		// Cold-start networking may cause retries before the first successful
		// refresh. A restarted bridge must reuse its valid persisted token.
		if err == nil && ((expected == 0 && attempts == 0) || (expected > 0 && attempts >= float64(expected))) {
			fmt.Printf("Fixture OAuth attempts: %g (minimum %d; zero required for token reuse).\n", attempts, expected)
			return nil
		}
		return fmt.Errorf("fixture OAuth attempts %q do not satisfy expected %d (zero exact, positive minimum)", value, expected)
	}
	return errors.New("fixture OAuth attempt metric missing")
}
