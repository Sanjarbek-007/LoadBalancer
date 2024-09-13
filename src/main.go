package main

import (
	"balancer/logger"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"sync"
	"time"
)

type Server interface {
	Address() string
	IsAlive() bool
	Serve(rw http.ResponseWriter, req *http.Request)
}

type ProxyServer struct {
	Addr      string
	Proxy     *httputil.ReverseProxy
	alive     bool
	checkFreq time.Duration
	mu        sync.Mutex
}

func newProxyServer(addr string, checkFreq time.Duration) *ProxyServer {
	serverUrl, err := url.Parse(addr)
	if err != nil {
		handleErr(err)
	}

	server := &ProxyServer{
		Addr:      addr,
		Proxy:     httputil.NewSingleHostReverseProxy(serverUrl),
		checkFreq: checkFreq,
	}
	go server.monitorHealth()
	return server
}

func (s *ProxyServer) monitorHealth() {
	for {
		s.mu.Lock()
		s.alive = s.IsAlive()
		s.mu.Unlock()
		time.Sleep(s.checkFreq)
	}
}

func (s *ProxyServer) Address() string { return s.Addr }

func (s *ProxyServer) IsAlive() bool {
	fmt.Printf("Checking server: %s\n", s.Addr)
	client := &http.Client{
		Timeout: 2 * time.Second,
	}
	resp, err := client.Get(s.Addr + "/health")
	if err != nil || resp.StatusCode >= 400 {
		fmt.Printf("Server %s is down\n", s.Addr)
		logger.NewLogger().Info("Server %s is down\n", s.Addr, "Info")
		return false
	}
	fmt.Printf("Server %s is up\n", s.Addr)
	logger.NewLogger().Info("Server %s is up\n", s.Addr, "Info")
	return true
}

func (s *ProxyServer) Serve(rw http.ResponseWriter, req *http.Request) {
	s.Proxy.ServeHTTP(rw, req)
}

type LoadBalancer struct {
	Port            string
	RoundRobinCount int
	Servers         []Server
	mu              sync.Mutex
}

func NewLoadBalancer(port string, servers []Server) *LoadBalancer {
	return &LoadBalancer{
		Port:            port,
		RoundRobinCount: 0,
		Servers:         servers,
	}
}

func handleErr(err error) {
	if err != nil {
		fmt.Printf("error: %v\n", err)
		logger.NewLogger().Error("error:", slog.String("key", err.Error()))
		os.Exit(1)
	}
}

// round-robin
func (lb *LoadBalancer) GetNextAvailableServer() Server {
	lb.mu.Lock()
	defer lb.mu.Unlock()

	server := lb.Servers[lb.RoundRobinCount%len(lb.Servers)]
	for !server.IsAlive() {
		lb.RoundRobinCount++
		server = lb.Servers[lb.RoundRobinCount%len(lb.Servers)]
		time.Sleep(1 * time.Second)
	}
	lb.RoundRobinCount++
	return server
}

func (lb *LoadBalancer) ServeProxy(rw http.ResponseWriter, req *http.Request) {
	TargetServer := lb.GetNextAvailableServer()
	fmt.Printf("Forwarding request to address: %s\n", TargetServer.Address())
	logger.NewLogger().Info("Forwarding request to address: %s\n", TargetServer.Address(), "Good")

	respRec := httptest.NewRecorder()
	TargetServer.Serve(respRec, req)

	resp := respRec.Result()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		http.Error(rw, "Failed to forward request", http.StatusInternalServerError)
		logger.NewLogger().Error("Failed to forward request")
		return
	}

	for key, values := range resp.Header {
		for _, value := range values {
			rw.Header().Add(key, value)
		}
	}
	rw.WriteHeader(resp.StatusCode)
	io.Copy(rw, resp.Body)
}

func main() {
	servers := []Server{
		newProxyServer("http://3.75.208.130:8081", 10*time.Second),
		newProxyServer("http://3.120.39.160:8082", 10*time.Second),
	}

	lb := NewLoadBalancer("8080", servers)

	handleRedirect := func(rw http.ResponseWriter, req *http.Request) {
		lb.ServeProxy(rw, req)
	}

	http.HandleFunc("/", handleRedirect)

	fmt.Printf("Serving request at 'localhost:%s'\n", lb.Port)
	logger.NewLogger().Info("Serving request at 'localhost:%s'\n", lb.Port, "OK")
	err := http.ListenAndServe(":"+lb.Port, nil)
	handleErr(err)
}
