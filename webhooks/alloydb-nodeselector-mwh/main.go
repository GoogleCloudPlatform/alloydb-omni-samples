package main

import (
	"crypto/tls"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/rmishgoog/alloydb-nodelselector-mwh/handlers"
)

func main() {

	handlers.BuildSelectors()
	handlers.Routes()

	tlsCertRoot := os.Getenv("TLS_CERT_ROOT_DIR")
	if tlsCertRoot == "" {
		log.Fatalf("main()::TLS_CERT_ROOT_DIR environment variables must be set, could not load the certifcates, exiting")
	}

	certFile := filepath.Join(tlsCertRoot, "tls.crt")
	keyFile := filepath.Join(tlsCertRoot, "tls.key")

	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		log.Fatalf("main()::Could not load TLS certificates, exiting with error %v", err)
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
	}
	port := os.Getenv("CONTAINER_PORT")
	if port == "" {
		port = "8443"
	}
	tlsServer := &http.Server{
		Addr:      "" + ":" + port,
		TLSConfig: tlsConfig,
	}
	if err := tlsServer.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
		log.Fatalf("main()::Could not start the webhook server at port %s, exiting with error %v", port, err)
	}

}
