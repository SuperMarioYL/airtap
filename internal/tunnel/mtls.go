// Package tunnel provides mutual-TLS plumbing between the Airtap thin
// client and airtapd. The CA is the trust anchor: both peers verify the
// other's leaf against the CA pool. Hostname verification is replaced
// with explicit chain verification so the cert need not enumerate every
// box IP (box addresses are operator-supplied, often raw IPs).
package tunnel

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"time"
)

// DialTimeout is how long the thin client waits for the mTLS handshake.
const DialTimeout = 10 * time.Second

// Dial opens an mTLS connection to addr. caPath is the CA used to verify
// the daemon's leaf cert; certPath/keyPath is the client's own leaf used
// for client authentication.
func Dial(addr, caPath, certPath, keyPath string) (*tls.Conn, error) {
	pool, err := loadCAPool(caPath)
	if err != nil {
		return nil, fmt.Errorf("tunnel: load CA: %w", err)
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("tunnel: load client keypair: %w", err)
	}
	config := &tls.Config{
		Certificates: []tls.Certificate{cert},
		// We skip Go's built-in hostname check (box addrs are raw IPs that
		// need not appear in the leaf SAN) and instead verify the peer leaf
		// chains to our CA. A peer without a CA-signed leaf is rejected.
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			return verifyPeer(rawCerts, pool)
		},
		MinVersion: tls.VersionTLS12,
	}
	d := net.Dialer{Timeout: DialTimeout}
	raw, err := d.Dial("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("tunnel: dial %s: %w", addr, err)
	}
	tlsConn := tls.Client(raw, config)
	if err := tlsConn.Handshake(); err != nil {
		raw.Close()
		return nil, fmt.Errorf("tunnel: handshake %s: %w", addr, err)
	}
	return tlsConn, nil
}

// Listen starts an mTLS server on addr. caPath verifies client leafs
// (RequireAndVerifyClientCert); certPath/keyPath is the server leaf the
// client checks against the same CA.
func Listen(addr, caPath, certPath, keyPath string) (net.Listener, error) {
	pool, err := loadCAPool(caPath)
	if err != nil {
		return nil, fmt.Errorf("tunnel: load CA: %w", err)
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("tunnel: load server keypair: %w", err)
	}
	config := &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
		MinVersion:   tls.VersionTLS12,
	}
	ln, err := tls.Listen("tcp", addr, config)
	if err != nil {
		return nil, fmt.Errorf("tunnel: listen %s: %w", addr, err)
	}
	return ln, nil
}

// GenCA generates a self-signed ECDSA P-256 CA. The returned cert and
// key are PEM-encoded. The key is the CA's signing key and is only ever
// needed where new leafs are minted (the laptop running `airtap init`).
func GenCA() (cert, key []byte, err error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("tunnel: ca keygen: %w", err)
	}
	serial, err := randSerial()
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "airtap-ca"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return nil, nil, fmt.Errorf("tunnel: ca create: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, nil, fmt.Errorf("tunnel: ca key marshal: %w", err)
	}
	cert = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	key = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return cert, key, nil
}

// GenClientCert mints an ECDSA P-256 leaf signed by the given CA. The
// leaf carries both ClientAuth and ServerAuth extended key usages and
// SANs for localhost/127.0.0.1, so a single generated leaf can serve
// as the client cert (laptop) and as the daemon's server cert (the leaf
// is the second artifact produced by `airtap init`). Returned values
// are PEM-encoded cert and key.
func GenClientCert(caCertPEM, caKeyPEM []byte) (cert, key []byte, err error) {
	caCert, err := parseCert(caCertPEM)
	if err != nil {
		return nil, nil, fmt.Errorf("tunnel: parse CA cert: %w", err)
	}
	caKey, err := parseSigner(caKeyPEM)
	if err != nil {
		return nil, nil, fmt.Errorf("tunnel: parse CA key: %w", err)
	}
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("tunnel: leaf keygen: %w", err)
	}
	serial, err := randSerial()
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "airtap"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageClientAuth,
			x509.ExtKeyUsageServerAuth,
		},
		DNSNames:    []string{"localhost"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &priv.PublicKey, caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("tunnel: leaf create: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, nil, fmt.Errorf("tunnel: leaf key marshal: %w", err)
	}
	cert = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	key = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return cert, key, nil
}

// loadCAPool reads a CA PEM file into an x509 cert pool.
func loadCAPool(caPath string) (*x509.CertPool, error) {
	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		return nil, fmt.Errorf("read CA %s: %w", caPath, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("no CA certs parsed from %s", caPath)
	}
	return pool, nil
}

// verifyPeer checks that the peer presented at least one leaf that
// chains to the CA pool. Used as the client's VerifyPeerCertificate
// callback under InsecureSkipVerify.
func verifyPeer(rawCerts [][]byte, pool *x509.CertPool) error {
	if len(rawCerts) == 0 {
		return errors.New("tunnel: peer presented no certificate")
	}
	leafs := make([]*x509.Certificate, 0, len(rawCerts))
	for _, raw := range rawCerts {
		c, err := x509.ParseCertificate(raw)
		if err != nil {
			return fmt.Errorf("tunnel: parse peer cert: %w", err)
		}
		leafs = append(leafs, c)
	}
	// Add any intermediates the peer sent (after the leaf) to the chain.
	intermediates := x509.NewCertPool()
	for _, c := range leafs[1:] {
		intermediates.AddCert(c)
	}
	if _, err := leafs[0].Verify(x509.VerifyOptions{
		Roots:         pool,
		Intermediates: intermediates,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}); err != nil {
		return fmt.Errorf("tunnel: peer cert not signed by CA: %w", err)
	}
	return nil
}

// randSerial returns a random 128-bit positive serial number.
func randSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("tunnel: serial: %w", err)
	}
	return serial, nil
}

// parseCert decodes a PEM cert block into an x509 certificate.
func parseCert(pemBytes []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("no PEM block in cert")
	}
	return x509.ParseCertificate(block.Bytes)
}

// parseSigner decodes a PEM private key (EC SEC1, PKCS1 RSA, or PKCS8)
// into a crypto.Signer suitable for x509.CreateCertificate.
func parseSigner(pemBytes []byte) (crypto.Signer, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("no PEM block in key")
	}
	switch block.Type {
	case "EC PRIVATE KEY":
		k, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		return k, nil
	case "RSA PRIVATE KEY":
		k, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		return k, nil
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		if s, ok := k.(crypto.Signer); ok {
			return s, nil
		}
		return nil, errors.New("PKCS8 key is not a signer")
	}
	return nil, fmt.Errorf("unsupported key PEM type %q", block.Type)
}

