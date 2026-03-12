package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"os"
	"time"

	"github.com/vibed-project/vibeD/internal/config"
)

// NewTLSConfig creates a *tls.Config based on the TLS configuration.
//
// If TLS is not enabled, returns nil.
// If certFile/keyFile are provided, loads from disk.
// If autoTLS is enabled, generates a self-signed certificate for development.
func NewTLSConfig(cfg config.TLSConf, logger *slog.Logger) (*tls.Config, error) {
	if !cfg.Enabled {
		return nil, nil
	}

	if cfg.CertFile != "" && cfg.KeyFile != "" {
		// Load certificate from disk
		cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("loading TLS certificate: %w", err)
		}

		logger.Info("TLS enabled with certificate files",
			"cert", cfg.CertFile,
			"key", cfg.KeyFile,
		)

		return &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}, nil
	}

	if cfg.AutoTLS {
		// Generate self-signed certificate for development
		cert, err := generateSelfSignedCert()
		if err != nil {
			return nil, fmt.Errorf("generating self-signed certificate: %w", err)
		}

		logger.Warn("TLS enabled with auto-generated self-signed certificate (NOT for production)")

		return &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}, nil
	}

	return nil, fmt.Errorf("TLS enabled but no certificate configured: set certFile/keyFile or enable autoTLS")
}

// generateSelfSignedCert creates a self-signed TLS certificate for development.
func generateSelfSignedCert() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generating private key: %w", err)
	}

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generating serial number: %w", err)
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"vibeD Development"},
			CommonName:   "localhost",
		},
		NotBefore: time.Now().Add(-1 * time.Hour),
		NotAfter:  time.Now().Add(365 * 24 * time.Hour),

		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,

		IPAddresses: []net.IP{
			net.ParseIP("127.0.0.1"),
			net.ParseIP("::1"),
		},
		DNSNames: []string{
			"localhost",
			"vibed",
			"vibed.local",
		},
	}

	// Also add hostname if available
	if hostname, err := os.Hostname(); err == nil {
		template.DNSNames = append(template.DNSNames, hostname)
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("creating certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("marshaling private key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	return tls.X509KeyPair(certPEM, keyPEM)
}
