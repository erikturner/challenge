package main

import (
	"crypto/rand"
	"crypto/rsa"
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

func main() {
	out := flag.String("out", "certs", "output directory")
	flag.Parse()
	if err := generate(*out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func generate(out string) error {
	if err := os.MkdirAll(out, 0o700); err != nil {
		return err
	}
	now := time.Now().Add(-time.Minute)
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	caCert := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "kube-replica-server-local-ca"},
		NotBefore: now, NotAfter: now.Add(24 * time.Hour), IsCA: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign, BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caCert, caCert, &caKey.PublicKey, caKey)
	if err != nil {
		return err
	}
	if err := writeCert(filepath.Join(out, "ca.crt"), caDER); err != nil {
		return err
	}

	if err := issue(out, "server", 2, caCert, caKey, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, []string{"localhost", "kube-replica-server"}, []net.IP{net.ParseIP("127.0.0.1")}, now); err != nil {
		return err
	}
	if err := issue(out, "client", 3, caCert, caKey, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, nil, nil, now); err != nil {
		return err
	}
	return nil
}

func issue(out, name string, serial int64, ca *x509.Certificate, caKey *rsa.PrivateKey, usage []x509.ExtKeyUsage, dns []string, ips []net.IP, now time.Time) error {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	cert := &x509.Certificate{
		SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: name}, NotBefore: now,
		NotAfter: now.Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: usage, DNSNames: dns, IPAddresses: ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, ca, &key.PublicKey, caKey)
	if err != nil {
		return err
	}
	if err := writeCert(filepath.Join(out, name+".crt"), der); err != nil {
		return err
	}
	return writeKey(filepath.Join(out, name+".key"), key)
}

func writeCert(path string, der []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	return pem.Encode(f, &pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func writeKey(path string, key *rsa.PrivateKey) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	return pem.Encode(f, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}
