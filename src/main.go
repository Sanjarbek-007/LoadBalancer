package main

import (
	"fmt"
	"io"
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
		defer s.mu.Unlock()
		s.alive = s.IsAlive()
		time.Sleep(s.checkFreq)
	}
}


func (s *ProxyServer) Address() string { return s.Addr }


func (s *ProxyServer) IsAlive() bool {
	fmt.Printf("Checking server: %s\n", s.Addr)
	client := &http.Client{
		Timeout: 2 * time.Second,
	}
	resp, err := client.Get(s.Addr)
	if err != nil || resp.StatusCode >= 400 {
		fmt.Printf("Server %s is down\n", s.Addr)
		return false
	}
	fmt.Printf("Server %s is up\n", s.Addr)
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


	respRec := httptest.NewRecorder()
	TargetServer.Serve(respRec, req)


	resp := respRec.Result()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		http.Error(rw, "Failed to forward request", http.StatusInternalServerError)
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
		newProxyServer("http://3.75.208.130:2345/swagger/index.html", 10*time.Second),
		newProxyServer("http://3.65.0.245:2345/swagger/index.html", 10*time.Second),
		newProxyServer("http://3.65.0.245:9876/swagger/index.html", 10*time.Second),
	}

	lb := NewLoadBalancer("8080", servers)

	handleRedirect := func(rw http.ResponseWriter, req *http.Request) {
		lb.ServeProxy(rw, req)
	}

	http.HandleFunc("/", handleRedirect)

	fmt.Printf("Serving request at 'localhost:%s'\n", lb.Port)
	err := http.ListenAndServe(":"+lb.Port, nil)
	handleErr(err)
}
