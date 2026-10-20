// aeo-tls creates short-lived TLS material for the disposable browser-test
// provider. Nothing is installed into the machine's certificate trust store.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"flag"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

func writeFixture(directory string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	certificate := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "GopherCRM temporary AEO test peer"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(6 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, &key.PublicKey, key)
	if err != nil {
		return err
	}
	privateKey, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	for name, block := range map[string]*pem.Block{
		"certificate.pem": {Type: "CERTIFICATE", Bytes: der},
		"private-key.pem": {Type: "PRIVATE KEY", Bytes: privateKey},
	} {
		if err := os.WriteFile(filepath.Join(directory, name), pem.EncodeToMemory(block), 0600); err != nil {
			return err
		}
	}
	return nil
}

func main() {
	directory := flag.String("directory", "", "private temporary directory for generated test TLS files")
	flag.Parse()
	if *directory == "" {
		fmt.Fprintln(os.Stderr, "aeo-tls: --directory is required")
		os.Exit(2)
	}
	if err := writeFixture(*directory); err != nil {
		fmt.Fprintln(os.Stderr, "aeo-tls:", err)
		os.Exit(1)
	}
}
