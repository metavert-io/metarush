// Command catalogueserve serves a folder over HTTPS on this computer's
// loopback for browser runs of community catalogues of abilities
// (scripts/ability-catalogue-e2e.sh, D480–D486): a certificate made for this
// run alone (127.0.0.1), whose authority it writes to a file MetaRush trusts
// only under go test and in browser runs (METARUSH_CATALOGUE_CA). It is a
// development helper (go run ./scripts/catalogueserve), not part of the
// metarush binary.
//
//	go run ./scripts/catalogueserve <folder> <port> <ca.pem>
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"time"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: go run ./scripts/catalogueserve <folder> <port> <ca.pem>")
		os.Exit(2)
	}
	dir, port, caOut := os.Args[1], os.Args[2], os.Args[3]
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must(err)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "MetaRush catalogue test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(48 * time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	must(err)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	must(os.WriteFile(caOut, certPEM, 0o600))
	kb, err := x509.MarshalECPrivateKey(key)
	must(err)
	cert, err := tls.X509KeyPair(certPEM, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}))
	must(err)
	srv := &http.Server{Addr: "127.0.0.1:" + port, Handler: http.FileServer(http.Dir(dir)), ReadHeaderTimeout: 10 * time.Second,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}}
	fmt.Printf("serving %s on https://127.0.0.1:%s (its authority: %s)\n", dir, port, caOut)
	must(srv.ListenAndServeTLS("", ""))
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
