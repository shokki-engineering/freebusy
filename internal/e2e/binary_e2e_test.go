// The binary suite: the real server, built and started as a process with the
// dev configuration, driven only through what a client sees -- the REST
// gateway, an MCP listener, and webhook deliveries arriving at a receiver.
// It is the one suite that exercises the wiring the in-process suites skip:
// gateway and MCP registration, and the background delivery loop.
//
//	FREEBUSY_TEST_POSTGRES_DSN=... FREEBUSY_TEST_GRAPHQL_URL=... go test ./internal/e2e/ -run Binary -v
package e2e

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/webhook/v1/webhookpbv1"
)

func TestBinary_Gorm(t *testing.T) {
	openTestGorm(t) // the gate: a live Postgres
	binaryFlow(t, "gorm")
}

func TestBinary_Hasura(t *testing.T) {
	connectTestGraphQL(t) // the gate: a live DDN engine
	binaryFlow(t, "hasura")
}

var (
	buildOnce sync.Once
	binary    string
	buildErr  error
)

// build compiles the server once per test run.
func build(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "freebusy-bin-")
		if err != nil {
			buildErr = err
			return
		}
		binary = filepath.Join(dir, "freebusy")
		cmd := exec.Command("go", "build", "-o", binary, ".")
		cmd.Dir = repoRoot(t)
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("go build: %v\n%s", err, out)
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return binary
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// freePorts finds n consecutive free loopback ports: gRPC, HTTP and MCP.
func freePorts(t *testing.T, n int) int {
	t.Helper()
	for base := 21000 + int(time.Now().UnixNano()%20000); base < 60000; base += n {
		ok := true
		for p := base; p < base+n && ok; p++ {
			l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
			if err != nil {
				ok = false
				continue
			}
			_ = l.Close()
		}
		if ok {
			return base
		}
	}
	t.Fatal("no free port range")
	return 0
}

// client speaks to the server's HTTP port, which the dev configuration serves
// over TLS -- here with the throwaway certificate selfSign made. Trusting it
// is what a browser exception would do; the test checks the API, not the
// certificate.
var client = &http.Client{
	Timeout:   10 * time.Second,
	Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec // local dev certificate
}

// server is the running binary.
type server struct {
	t    *testing.T
	base string // REST
	mcp  int    // the first MCP port
	out  *bytes.Buffer
}

// start runs the binary from a scratch directory whose dev overlay is the
// repository's, with HTTP on, ports of its own, provider chosen, and delivery
// rounds every second.
func start(t *testing.T, provider string) *server {
	t.Helper()
	bin := build(t)
	root := repoRoot(t)
	overlay, err := os.ReadFile(filepath.Join(root, "config", "freebusy.dev.toml"))
	if err != nil {
		t.Fatal(err)
	}
	ports := freePorts(t, 3)
	grpcPort, httpPort, mcpPort := ports, ports+1, ports+2
	cfg := string(overlay)
	for _, r := range []struct{ re, repl string }{
		{`(?m)^enable_http = .*$`, `enable_http = true`},
		{`(?m)^\[server\.grpc\]\nhost = .*\nport = .*$`, fmt.Sprintf("[server.grpc]\nhost = \"127.0.0.1\"\nport = %d", grpcPort)},
		{`(?m)^\[server\.http\]\nhost = .*\nport = .*$`, fmt.Sprintf("[server.http]\nhost = \"127.0.0.1\"\nport = %d", httpPort)},
		{`(?m)^\[server\.mcp\]\nhost = .*\nport = .*$`, fmt.Sprintf("[server.mcp]\nhost = \"127.0.0.1\"\nport = %d", mcpPort)},
		{`(?m)^provider = .*$`, fmt.Sprintf("provider = %q", provider)},
		{`(?m)^interval_seconds = .*$`, `interval_seconds = 1`},
		// A certificate of the test's own: .certs is a developer's mkcert
		// pair, not in the repository, so CI has none.
		{`(?m)^cert_file = .*$`, `cert_file = "tls/cert.pem"`},
		{`(?m)^key_file = .*$`, `key_file = "tls/key.pem"`},
	} {
		re := regexp.MustCompile(r.re)
		if !re.MatchString(cfg) {
			t.Fatalf("dev overlay has no %s to override", r.re)
		}
		cfg = re.ReplaceAllString(cfg, r.repl)
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config", "freebusy.dev.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	selfSign(t, filepath.Join(dir, "tls"))
	for _, name := range []string{".env"} {
		if _, err := os.Stat(filepath.Join(root, name)); err == nil {
			_ = os.Symlink(filepath.Join(root, name), filepath.Join(dir, name))
		}
	}

	out := &bytes.Buffer{}
	cmd := exec.Command(bin)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = out, out
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("FREEBUSY_GRPC_PORT=%d", grpcPort), fmt.Sprintf("FREEBUSY_HTTP_PORT=%d", httpPort))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	s := &server{t: t, base: fmt.Sprintf("https://127.0.0.1:%d", httpPort), mcp: mcpPort, out: out}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGINT)
		select {
		case err := <-exited:
			if err != nil {
				t.Errorf("server exited with %v\n%s", err, tail(out))
			}
		case <-time.After(20 * time.Second):
			_ = cmd.Process.Kill()
			t.Errorf("server did not stop on SIGINT\n%s", tail(out))
		}
	})
	until(t, 60*time.Second, "the server to answer /health", func() bool {
		select {
		case err := <-exited:
			exited <- err
			t.Fatalf("the server exited at startup: %v\n%s", err, tail(out))
		default:
		}
		resp, err := client.Get(s.base + "/health")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, out)
	return s
}

// selfSign writes a throwaway certificate for 127.0.0.1 into dir.
func selfSign(t *testing.T, dir string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "freebusy e2e"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, block := range map[string]*pem.Block{
		"cert.pem": {Type: "CERTIFICATE", Bytes: der},
		"key.pem":  {Type: "EC PRIVATE KEY", Bytes: keyDER},
	} {
		if err := os.WriteFile(filepath.Join(dir, name), pem.EncodeToMemory(block), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func tail(b *bytes.Buffer) string {
	s := b.String()
	if len(s) > 4000 {
		s = "..." + s[len(s)-4000:]
	}
	return s
}

func until(t *testing.T, within time.Duration, what string, ok func() bool, out *bytes.Buffer) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s\n%s", what, tail(out))
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// call sends a REST request and decodes a JSON object reply.
func (s *server) call(method, path string, body any, want int) map[string]any {
	s.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, s.base+path, rd)
	if err != nil {
		s.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		s.t.Fatalf("%s %s: %d, want %d: %s", method, path, resp.StatusCode, want, raw)
	}
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return out
}

func binaryFlow(t *testing.T, provider string) {
	s := start(t, provider)
	rec := &receiver{}
	endpoint := httptest.NewServer(rec)
	defer endpoint.Close()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000_000)
	hook := "webhooks/bin-" + suffix
	court := "resources/bin-court-" + suffix

	// REST: create returns the secret, reads do not.
	created := s.call("POST", "/v1/webhooks?webhookId=bin-"+suffix, map[string]any{
		"uri": endpoint.URL + "/hook", "eventTypes": []string{"freebusy.resource.created", "freebusy.resource.deleted"},
	}, http.StatusOK)
	secret, _ := created["secret"].(string)
	if created["name"] != hook || !strings.HasPrefix(secret, "whsec_") || created["state"] != "WEBHOOK_STATE_ACTIVE" {
		t.Fatalf("created: %v", created)
	}
	rec.mu.Lock()
	rec.secret = secret
	rec.mu.Unlock()
	defer func() {
		req, _ := http.NewRequest("DELETE", s.base+"/v1/"+hook+"?force=true", nil)
		if resp, err := client.Do(req); err == nil {
			resp.Body.Close()
		}
	}()
	// The gateway emits unpopulated fields, so a withheld secret reads "".
	if got := s.call("GET", "/v1/"+hook, nil, http.StatusOK); got["secret"] != "" || got["previousSecret"] != "" {
		t.Fatalf("GET returned the secret: %v", got)
	}
	if got := s.call("PATCH", "/v1/"+hook+"?updateMask=displayName", map[string]any{"displayName": "binary e2e"}, http.StatusOK); got["displayName"] != "binary e2e" {
		t.Fatalf("PATCH: %v", got)
	}
	s.call("POST", "/v1/webhooks", map[string]any{"uri": "ftp://example.com"}, http.StatusBadRequest)

	// A resource created over REST reaches the receiver through the server's
	// own delivery loop, signed.
	s.call("POST", "/v1/resources?resourceId=bin-court-"+suffix, map[string]any{"displayName": "Court"}, http.StatusOK)
	arrived := func(eventType string) func() bool {
		return func() bool {
			rec.mu.Lock()
			defer rec.mu.Unlock()
			for _, ev := range rec.got {
				if ev["type"] == eventType && ev["subject"] == court {
					return true
				}
			}
			return false
		}
	}
	until(t, 20*time.Second, "freebusy.resource.created at the receiver", arrived("freebusy.resource.created"), s.out)
	deliveries := s.call("GET", "/v1/"+hook+"/deliveries", nil, http.StatusOK)["deliveries"].([]any)
	first := deliveries[0].(map[string]any)
	if len(deliveries) != 1 || first["state"] != "DELIVERY_STATE_SUCCEEDED" || first["subject"] != court {
		t.Fatalf("deliveries: %v", deliveries)
	}

	// Rotated over REST: the next delivery carries both signatures.
	rotated := s.call("POST", "/v1/"+hook+":rotateSecret", map[string]any{}, http.StatusOK)
	next, _ := rotated["secret"].(string)
	if next == "" || next == secret {
		t.Fatalf("rotate: %v", rotated)
	}
	rec.mu.Lock()
	rec.secret = next
	rec.mu.Unlock()
	s.call("DELETE", "/v1/"+court, nil, http.StatusOK)
	until(t, 20*time.Second, "freebusy.resource.deleted at the receiver", arrived("freebusy.resource.deleted"), s.out)
	rec.mu.Lock()
	if len(rec.bad) > 0 || rec.sigs[len(rec.sigs)-1] != 2 {
		t.Fatalf("signatures: %d bad, counts %v", len(rec.bad), rec.sigs)
	}
	rec.mu.Unlock()

	// Pause, resume, retry over REST.
	if got := s.call("POST", "/v1/"+hook+":pause", map[string]any{}, http.StatusOK); got["state"] != "WEBHOOK_STATE_PAUSED" {
		t.Fatalf("pause: %v", got)
	}
	if got := s.call("POST", "/v1/"+hook+":resume", map[string]any{}, http.StatusOK); got["state"] != "WEBHOOK_STATE_ACTIVE" {
		t.Fatalf("resume: %v", got)
	}
	if got := s.call("POST", "/v1/"+first["name"].(string)+":retry", map[string]any{}, http.StatusOK); got["state"] != "DELIVERY_STATE_PENDING" {
		t.Fatalf("retry: %v", got)
	}

	// MCP: every service shares the MCP port, each at its own path.
	// Validation runs on tool calls, and a read carries no secret.
	session := dialMCP(t, fmt.Sprintf("http://127.0.0.1:%d%s", s.mcp, webhookpbv1.WebhookServiceMCPDefaultBasePath))
	tools, err := session.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	for _, want := range []string{"webhook_service-create_webhook_v1", "webhook_service-rotate_webhook_secret_v1", "webhook_service-retry_delivery_v1"} {
		if !strings.Contains(strings.Join(names, " "), want) {
			t.Fatalf("MCP tools %v lack %s", names, want)
		}
	}
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "webhook_service-get_webhook_v1", Arguments: map[string]any{"name": hook}})
	if err != nil || res.IsError {
		t.Fatalf("MCP get_webhook: %v %v", err, toolText(res))
	}
	if text := toolText(res); !strings.Contains(text, hook) || strings.Contains(text, "whsec_") {
		t.Fatalf("MCP get_webhook: %s", text)
	}
	res, err = session.CallTool(context.Background(), &mcp.CallToolParams{Name: "webhook_service-list_deliveries_v1", Arguments: map[string]any{"parent": "not-a-webhook"}})
	if err != nil || !res.IsError {
		t.Fatalf("MCP accepted an invalid parent: %v %v", err, toolText(res))
	}

	// Deleting needs force once there are deliveries.
	s.call("DELETE", "/v1/"+hook, nil, http.StatusBadRequest)
	s.call("DELETE", "/v1/"+hook+"?force=true", nil, http.StatusOK)
	s.call("GET", "/v1/"+hook, nil, http.StatusNotFound)
}
