package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"
)

type Response struct {
	Domain string   `json:"domain"`
	Type   string   `json:"type"`
	Server string   `json:"server"`
	Count  int      `json:"count"`
	Values []string `json:"values"`
	Error  string   `json:"error,omitempty"`
}

// loggingResponseWriter captures the status code and response body for logging.
type loggingResponseWriter struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (lrw *loggingResponseWriter) WriteHeader(code int) {
	lrw.status = code
	lrw.ResponseWriter.WriteHeader(code)
}

func (lrw *loggingResponseWriter) Write(b []byte) (int, error) {
	lrw.body.Write(b) //nolint:errcheck
	return lrw.ResponseWriter.Write(b)
}

func loggingMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		lrw := &loggingResponseWriter{ResponseWriter: w, status: http.StatusOK}

		log.Printf("--> %s %s from %s", r.Method, r.URL, r.RemoteAddr)

		next(lrw, r)

		duration := time.Since(start)
		log.Printf("<-- %d %s (%s): %s", lrw.status, r.URL.Path, duration, lrw.body.String())
	}
}

func getEnv(key, defaultValue string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return defaultValue
}

func getResolver(dnsServer string) *net.Resolver {
	dialer := &net.Dialer{
		Timeout: 3 * time.Second,
	}

	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			return dialer.DialContext(ctx, "udp", dnsServer+":53")
		},
	}
}

func dnsHandler(w http.ResponseWriter, r *http.Request) {
	domain := r.URL.Query().Get("domain")
	recordType := r.URL.Query().Get("type")
	dnsServer := r.URL.Query().Get("server")

	if domain == "" || recordType == "" || dnsServer == "" {
		http.Error(w, "missing domain, type or server", http.StatusBadRequest)
		return
	}

	resolver := getResolver(dnsServer)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp := Response{
		Domain: domain,
		Type:   recordType,
		Server: dnsServer,
	}

	switch recordType {
	case "A":
		ips, err := resolver.LookupIP(ctx, "ip", domain)
		if err != nil {
			resp.Error = err.Error()
			break
		}
		for _, ip := range ips {
			resp.Values = append(resp.Values, ip.String())
		}
		resp.Count = len(resp.Values)

	case "CNAME":
		cname, err := resolver.LookupCNAME(ctx, domain)
		if err != nil {
			resp.Error = err.Error()
			break
		}
		resp.Values = []string{cname}
		resp.Count = 1

	case "SRV":
		_, addrs, err := resolver.LookupSRV(ctx, "", "", domain)
		if err != nil {
			resp.Error = err.Error()
			break
		}
		for _, srv := range addrs {
			target := srv.Target + ":" + strconv.Itoa(int(srv.Port))
			resp.Values = append(resp.Values, target)
		}
		resp.Count = len(resp.Values)

	default:
		http.Error(w, "unsupported type", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("failed to encode response: %v", err)
	}
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte("OK")); err != nil {
		log.Printf("failed to write health response: %v", err)
	}
}

func main() {
	port := getEnv("PORT", "8081")

	http.HandleFunc("/dns", loggingMiddleware(dnsHandler))
	http.HandleFunc("/healthz", loggingMiddleware(healthHandler))

	log.Printf("Listening on :%s\n", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
