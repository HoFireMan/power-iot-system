package migrations

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"power-iot-backend/internal/testsupport"
)

const (
	d1lR2ProviderURI = "spiffe://power-iot/a3/d1l-provider"
	d1lR2RunnerURI   = "spiffe://power-iot/a3/d1l-runner"
	d1lR2RunbookURI  = "spiffe://power-iot/a3/deployment-runbook"
)

type d1lR2IssuedAuthorization struct {
	AuthorizationID string             `json:"authorization_id"`
	IssuerRequestID string             `json:"issuer_request_id"`
	AttemptID       string             `json:"attempt_id"`
	State           AuthorizationState `json:"state"`
	Epoch           int64              `json:"epoch"`
	Nonce           string             `json:"nonce"`
	ExpiresAt       time.Time          `json:"expires_at"`
	Scope           string             `json:"scope"`
	Bindings        map[string]string  `json:"bindings"`
	Envelope        string             `json:"envelope"`
	SecretAvailable bool               `json:"secret_available"`
}

type d1lR2IssueRequest struct {
	IssuerRequestID string            `json:"issuer_request_id"`
	AttemptID       string            `json:"attempt_id"`
	Scope           string            `json:"scope"`
	Bindings        map[string]string `json:"bindings"`
	TTLSeconds      int               `json:"ttl_seconds"`
}

type d1lR2InspectEvent struct {
	result InspectResult
	err    error
}

type d1lR2Provider struct {
	client       *AuthorizationClient
	release      <-chan struct{}
	inspectReady chan<- d1lR2InspectEvent

	mu                 sync.Mutex
	calls              []string
	attestationResults []AttestationResult
	attestationErrors  []error
	consumeResults     []ConsumeResult
	consumeErrors      []error
}

func (p *d1lR2Provider) Attestation(ctx context.Context) (AttestationResult, error) {
	p.mu.Lock()
	p.calls = append(p.calls, "attestation")
	p.mu.Unlock()
	result, err := p.client.Attestation(ctx)
	p.mu.Lock()
	p.attestationResults = append(p.attestationResults, result)
	p.attestationErrors = append(p.attestationErrors, err)
	p.mu.Unlock()
	if err != nil || result.Outcome != OutcomeSuccess {
		if err == nil {
			err = errors.New("provider attestation did not succeed")
		}
		p.inspectReady <- d1lR2InspectEvent{err: err}
	}
	return result, err
}

func (p *d1lR2Provider) Inspect(ctx context.Context, authorizationID string) (InspectResult, error) {
	result, err := p.client.Inspect(ctx, authorizationID)
	p.mu.Lock()
	p.calls = append(p.calls, "inspect")
	p.mu.Unlock()
	p.inspectReady <- d1lR2InspectEvent{result: result, err: err}
	<-p.release
	return result, err
}

func (p *d1lR2Provider) Consume(ctx context.Context, request ConsumeRequest) (ConsumeResult, error) {
	p.mu.Lock()
	p.calls = append(p.calls, "consume")
	p.mu.Unlock()
	result, err := p.client.Consume(ctx, request)
	p.mu.Lock()
	p.consumeResults = append(p.consumeResults, result)
	p.consumeErrors = append(p.consumeErrors, err)
	p.mu.Unlock()
	return result, err
}

func (p *d1lR2Provider) Resolve(ctx context.Context, request ResolveRequest) (ResolveResult, error) {
	p.mu.Lock()
	p.calls = append(p.calls, "resolve")
	p.mu.Unlock()
	return p.client.Resolve(ctx, request)
}

func (p *d1lR2Provider) snapshot() (calls []string, attestations []AttestationResult, attestationErrors []error, consumes []ConsumeResult, consumeErrors []error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.calls...), append([]AttestationResult(nil), p.attestationResults...), append([]error(nil), p.attestationErrors...), append([]ConsumeResult(nil), p.consumeResults...), append([]error(nil), p.consumeErrors...)
}

func TestD1LBootstrapInspectConsumeInterleavingFailsClosedPostgres(t *testing.T) {
	ctx := context.Background()
	targetDB, err := testsupport.New(ctx, os.Getenv("TEST_DATABASE_URL"), Up)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = targetDB.Close() })
	targetDSN := targetDB.DSN()
	target := d1lTargetForTest(t, targetDSN)
	evidence := []byte(strings.Repeat("e", 32))
	cfg := D1LBootstrapConfig{
		DatabaseURL:       targetDSN,
		TargetFingerprint: target,
		EvidenceDigest:    evidence,
		OperationID:       uuid.NewString(),
		AttemptID:         uuid.NewString(),
	}

	providerDB, providerDSN := d1lR2ProviderDatabase(t)
	securityURL, err := url.Parse(targetDSN)
	if err != nil {
		t.Fatal("security route metadata unavailable")
	}
	providerURL, err := url.Parse(providerDSN)
	if err != nil {
		t.Fatal("provider route metadata unavailable")
	}
	providerSourceURL, err := url.Parse(os.Getenv("D1L_PROVIDER_DATABASE_URL"))
	if err != nil {
		t.Fatal("provider source route metadata unavailable")
	}
	if providerURL.Port() != providerSourceURL.Port() {
		t.Fatalf("ROUTE-01 provider endpoint=%s:%s does not match D1L provider source port", providerURL.Hostname(), providerURL.Port())
	}
	if providerURL.Port() == securityURL.Port() || providerURL.Port() == "55434" {
		t.Fatalf("ROUTE-02 provider endpoint is not isolated from Security endpoint: provider=%s security=%s", providerURL.Port(), securityURL.Port())
	}
	if securityURL.Port() != "55434" {
		t.Fatalf("ROUTE-03 Security endpoint moved off TEST_DATABASE_URL dedicated port: %s", securityURL.Port())
	}
	if providerSourceURL.Host == securityURL.Host && providerSourceURL.Port() == securityURL.Port() {
		t.Fatal("ROUTE-04 D1L_PROVIDER_DATABASE_URL aliases Security endpoint")
	}
	if providerURL.Path == securityURL.Path || providerURL.Path == "/" {
		t.Fatal("ROUTE-05 provider fresh database is not distinct from Security database")
	}
	providerEndpoint, caFile, runnerCert, runnerKey, runbookCert, runbookKey := d1lR2ProviderRuntime(t, providerDSN)
	client, err := NewAuthorizationClient(AuthorizationClientConfig{
		Endpoint: providerEndpoint, TrustRootFile: caFile,
		ClientCertFile: runnerCert, ClientKeyFile: runnerKey,
		ExpectedProviderURI: d1lR2ProviderURI, Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	issue := d1lR2IssueAuthorization(t, providerEndpoint, caFile, runbookCert, runbookKey, d1lR2IssueRequest{
		IssuerRequestID: uuid.NewString(), AttemptID: cfg.AttemptID,
		Scope: ScopeControlCatalogInstall,
		Bindings: map[string]string{
			"operation":     cfg.OperationID,
			"attempt_id":    cfg.AttemptID,
			"target_id":     hexDigest(target),
			"installer_id":  D1LInstallerDigestV1,
			"evidence_hash": hexDigest(evidence),
		},
		TTLSeconds: 600,
	})
	if issue.State != AuthorizationIssued || !issue.SecretAvailable || issue.Envelope == "" {
		t.Fatalf("provider issue did not return one usable authorization: state=%s secret_available=%t", issue.State, issue.SecretAvailable)
	}
	cfg.AuthorizationID = issue.AuthorizationID

	releaseA := make(chan struct{})
	inspectReady := make(chan d1lR2InspectEvent, 1)
	provider := &d1lR2Provider{client: client, release: releaseA, inspectReady: inspectReady}
	cfg.Provider = provider
	cfg.Envelope = strings.NewReader(issue.Envelope)

	var trace []string
	var ddlCalls, manifestCalls, commitCalls int
	aDone := make(chan struct {
		report D1LBootstrapReport
		err    error
	}, 1)
	go func() {
		report, bootstrapErr := d1LBootstrapWithHooks(ctx, cfg, d1LBootstrapHooks{
			trace: func(stage string) { trace = append(trace, stage) },
			ddl:   func(context.Context, *sql.Tx) error { ddlCalls++; return errors.New("unexpected DDL hook") },
			manifest: func(context.Context, *sql.Tx, []byte, []byte, string) error {
				manifestCalls++
				return errors.New("unexpected manifest hook")
			},
			commit: func(context.Context, *sql.Tx) error { commitCalls++; return errors.New("unexpected commit hook") },
		})
		aDone <- struct {
			report D1LBootstrapReport
			err    error
		}{report: report, err: bootstrapErr}
	}()

	inspect := <-inspectReady
	if inspect.err != nil || inspect.result.Outcome != OutcomeSuccess || inspect.result.State != AuthorizationIssued {
		t.Fatalf("A_INSPECT_RESULT not PASS: result=%+v err=%v", inspect.result, inspect.err)
	}
	if got := d1lR2ProviderConsumeCount(t, providerDSN, issue.AuthorizationID); got != 0 {
		t.Fatalf("A_PREFLIGHT_CONSUME_COUNT=%d, want 0", got)
	}

	bRequest := ConsumeRequest{
		ConsumeRequestID: uuid.NewString(), AuthorizationID: issue.AuthorizationID,
		IssuerRequestID: issue.IssuerRequestID, Operation: cfg.OperationID,
		AttemptID: cfg.AttemptID, TargetID: hexDigest(target), InstallerID: D1LInstallerDigestV1,
		EvidenceHash: hexDigest(evidence), Scope: ScopeControlCatalogInstall,
		Nonce: issue.Nonce, Envelope: []byte(issue.Envelope), Epoch: issue.Epoch,
	}
	bResult, bErr := client.Consume(ctx, bRequest)
	if bErr != nil || bResult.Outcome != OutcomeSuccess || bResult.State != ConsumeConsumed {
		t.Fatalf("B_CONSUME not SUCCESS: result=%+v err=%v", bResult, bErr)
	}
	close(releaseA)
	a := <-aDone

	calls, attestations, attestationErrors, consumes, consumeErrors := provider.snapshot()
	if len(attestations) != 1 || len(attestationErrors) != 1 || attestations[0].Outcome != OutcomeSuccess || attestationErrors[0] != nil {
		t.Fatalf("A_ATTESTATION_RESULT not PASS: results=%+v errors=%v", attestations, attestationErrors)
	}
	if a.err == nil || !errors.Is(a.err, ErrD1LNoRetry) {
		t.Fatalf("A_FINAL_CONSUME did not fail closed without retry: report=%+v err=%v", a.report, a.err)
	}
	if a.report.InstallState != D1LInstallNotInstalled || a.report.After != D1LV5Base || a.report.Committed {
		t.Fatalf("A failure classified target incorrectly: report=%+v", a.report)
	}
	if got := strings.Join(trace, ","); got != "PIN,DERIVE,VERIFY,FENCE,LOCK,V5_BASE,CONSUME" {
		t.Fatalf("A protected path trace=%s, want PIN through real Consume and no DDL", got)
	}
	if strings.Join(calls, ",") != "attestation,inspect,consume" {
		t.Fatalf("A provider calls=%v, want Attestation, Inspect, one Consume", calls)
	}
	if len(consumes) != 1 || len(consumeErrors) != 1 || consumes[0].Outcome != OutcomeAlreadyConsumed || consumeErrors[0] != nil {
		t.Fatalf("A_FINAL_CONSUME result=%+v errors=%v, want one already-consumed rejection", consumes, consumeErrors)
	}
	if ddlCalls != 0 || manifestCalls != 0 || commitCalls != 0 {
		t.Fatalf("A side effects ddl=%d manifest=%d commit=%d, want all zero", ddlCalls, manifestCalls, commitCalls)
	}
	if a.report.ConsumeRequestID == "" || a.report.ConsumeRequestID == bRequest.ConsumeRequestID {
		t.Fatalf("A final Consume request identity=%q collides with B=%q", a.report.ConsumeRequestID, bRequest.ConsumeRequestID)
	}
	if strings.Contains(a.err.Error(), issue.Envelope) {
		t.Fatal("A error disclosed the protected envelope")
	}

	state, ownerConsumeID, intentCount, ownerIdentity, issueCount, privilegedReplay := d1lR2ProviderEvidence(t, providerDSN, issue.AuthorizationID, issue.IssuerRequestID, issue.Envelope)
	if state != "CONSUMED" || ownerConsumeID != bRequest.ConsumeRequestID {
		t.Fatalf("final Provider state=%s consume_request_id=%s, want CONSUMED by B=%s", state, ownerConsumeID, bRequest.ConsumeRequestID)
	}
	if intentCount != 1 || ownerIdentity != d1lR2RunnerURI {
		t.Fatalf("provider intent count=%d owner=%q, want one B runner intent", intentCount, ownerIdentity)
	}
	if issueCount != 1 {
		t.Fatalf("A_REISSUE_COUNT=%d, want 0 (one original issue row)", issueCount-1)
	}
	if privilegedReplay != 0 {
		t.Fatalf("A_PRIVILEGED_REPLAY=%d, want 0", privilegedReplay)
	}

	targetProbe, err := sql.Open("postgres", targetDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer targetProbe.Close()
	var controlExists bool
	if err := targetProbe.QueryRowContext(ctx, "SELECT to_regclass('security_control.control_schema_migrations') IS NOT NULL").Scan(&controlExists); err != nil {
		t.Fatal(err)
	}
	if controlExists {
		t.Fatal("A failure left D1-L control schema behind")
	}
	var version, metadataRows int
	var dirty bool
	if err := targetProbe.QueryRowContext(ctx, "SELECT version, dirty FROM schema_migrations").Scan(&version, &dirty); err != nil {
		t.Fatal(err)
	}
	if err := targetProbe.QueryRowContext(ctx, "SELECT count(*) FROM schema_migrations").Scan(&metadataRows); err != nil {
		t.Fatal(err)
	}
	if version != 5 || dirty || metadataRows != 1 {
		t.Fatalf("target metadata version=%d dirty=%t rows=%d, want V5_BASE", version, dirty, metadataRows)
	}

	t.Logf("R2 deterministic interleaving security_database=%s security_endpoint=%s:%s provider_database=%s provider_endpoint=%s:%s A_INSPECT_RESULT=PASS A_PREFLIGHT_CONSUME_COUNT=0 B_CONSUME=SUCCESS A_FINAL_CONSUME=REJECT A_FINAL_CONSUME_ATTEMPTS=1 A_CONSUME_RETRIES=0 A_REISSUE_COUNT=0 A_DDL_EXECUTIONS=0 A_MANIFEST_WRITES=0 A_COMMIT_ATTEMPTS=0 A_PRIVILEGED_REPLAY=0 final_provider_state=CONSUMED_by_B trace=%s", targetDB.Name(), securityURL.Hostname(), securityURL.Port(), providerDB, providerURL.Hostname(), providerURL.Port(), strings.Join(trace, ">"))
}

func d1lR2ProviderDatabase(t *testing.T) (string, string) {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv("D1L_PROVIDER_DATABASE_URL"))
	if raw == "" {
		t.Skip("D1L_PROVIDER_DATABASE_URL is not configured; isolated provider interleaving evidence skipped")
	}
	source, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	name := "d1l_provider_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	adminURL := *source
	adminURL.Path = "/postgres"
	adminURL.RawPath = ""
	admin, err := sql.Open("postgres", adminURL.String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(context.Background(), `CREATE DATABASE "`+name+`"`); err != nil {
		_ = admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), `DROP DATABASE IF EXISTS "`+name+`" WITH (FORCE)`)
		_ = admin.Close()
	})
	providerURL := *source
	// provider.LoadConfig intentionally rejects the target's literal loopback
	// host. The mapped loopback spelling reaches the same dedicated endpoint
	// while retaining that production boundary check.
	providerURL.Host = "[::ffff:127.0.0.1]:" + source.Port()
	providerURL.Path = "/" + name
	providerURL.RawPath = ""
	providerDSN := providerURL.String()
	probe, err := sql.Open("postgres", providerDSN)
	if err != nil {
		t.Fatal(err)
	}
	if err := probe.PingContext(context.Background()); err != nil {
		_ = probe.Close()
		t.Fatal(err)
	}
	_ = probe.Close()
	return name, providerDSN
}

type d1lR2Certificates struct {
	caPEM        []byte
	providerCert tls.Certificate
	runnerCert   tls.Certificate
	runbookCert  tls.Certificate
}

func d1lR2ProviderRuntime(t *testing.T, providerDSN string) (endpoint, caFile, runnerCertFile, runnerKeyFile, runbookCertFile, runbookKeyFile string) {
	t.Helper()
	dir := t.TempDir()
	certs := d1lR2MakeCertificates(t)
	caFile = filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(caFile, certs.caPEM, 0600); err != nil {
		t.Fatal(err)
	}
	providerCertFile, providerKeyFile := d1lR2WriteCertificate(t, dir, "provider", certs.providerCert)
	runnerCertFile, runnerKeyFile = d1lR2WriteCertificate(t, dir, "runner", certs.runnerCert)
	runbookCertFile, runbookKeyFile = d1lR2WriteCertificate(t, dir, "runbook", certs.runbookCert)

	binary := filepath.Join(dir, "d1l-authority")
	providerDir := d1lR2ProviderModuleDir(t)
	build := exec.Command("go", "build", "-tags", "securitytesthelper", "-o", binary, "./cmd/d1l-test-authority")
	build.Dir = providerDir
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build actual Provider authority: %v (%s)", err, strings.TrimSpace(string(output)))
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	endpoint = "https://" + address
	cmd := exec.Command(binary)
	cmd.Dir = providerDir
	cmd.Env = append(os.Environ(),
		"D1L_PROVIDER_DATABASE_URL="+providerDSN,
		"D1L_PROVIDER_HTTP_ADDR="+address,
		"D1L_PROVIDER_TLS_CERT_FILE="+providerCertFile,
		"D1L_PROVIDER_TLS_KEY_FILE="+providerKeyFile,
		"D1L_PROVIDER_TLS_CA_FILE="+caFile,
	)
	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = writePipe, writePipe
	if err := cmd.Start(); err != nil {
		_ = readPipe.Close()
		_ = writePipe.Close()
		t.Fatal(err)
	}
	_ = writePipe.Close()
	ready := make(chan error, 1)
	var logMu sync.Mutex
	var logLines []string
	go func() {
		scanner := bufio.NewScanner(readPipe)
		announced := false
		for scanner.Scan() {
			line := scanner.Text()
			logMu.Lock()
			logLines = append(logLines, line)
			logMu.Unlock()
			if strings.HasPrefix(line, "READY ") && !announced {
				announced = true
				ready <- nil
			}
		}
		if !announced {
			ready <- fmt.Errorf("Provider exited before readiness: %s", strings.Join(logLines, " | "))
		}
	}()
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal("actual Provider authority did not reach deterministic readiness")
	}
	return endpoint, caFile, runnerCertFile, runnerKeyFile, runbookCertFile, runbookKeyFile
}

func d1lR2ProviderModuleDir(t *testing.T) string {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate repository source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "../../../../power-iot-a3-deployment-control-plane"))
}

func d1lR2MakeCertificates(t *testing.T) d1lR2Certificates {
	t.Helper()
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "D1L R2 test CA"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	makeCert := func(uri string, server bool) tls.Certificate {
		key, e := rsa.GenerateKey(rand.Reader, 2048)
		if e != nil {
			t.Fatal(e)
		}
		template := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: uri}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), URIs: []*url.URL{mustD1LR2URI(t, uri)}, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment}
		if server {
			template.DNSNames = []string{"127.0.0.1"}
			template.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
			template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		} else {
			template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
		}
		der, e := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
		if e != nil {
			t.Fatal(e)
		}
		return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	}
	return d1lR2Certificates{caPEM: caPEM, providerCert: makeCert(d1lR2ProviderURI, true), runnerCert: makeCert(d1lR2RunnerURI, false), runbookCert: makeCert(d1lR2RunbookURI, false)}
}

func mustD1LR2URI(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func d1lR2WriteCertificate(t *testing.T, dir, name string, cert tls.Certificate) (certFile, keyFile string) {
	t.Helper()
	certFile = filepath.Join(dir, name+".crt.pem")
	keyFile = filepath.Join(dir, name+".key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), 0600); err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

func d1lR2IssueAuthorization(t *testing.T, endpoint, caFile, certFile, keyFile string, request d1lR2IssueRequest) d1lR2IssuedAuthorization {
	t.Helper()
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	caPEM, err := os.ReadFile(caFile)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		t.Fatal("test CA could not be loaded")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{cert}, ServerName: "127.0.0.1"}}
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Post(endpoint+"/v1/authorizations", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		t.Fatalf("actual Provider issue status=%d", resp.StatusCode)
	}
	var out d1lR2IssuedAuthorization
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func d1lR2ProviderConsumeCount(t *testing.T, dsn, authorizationID string) int {
	t.Helper()
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRowContext(context.Background(), "SELECT count(*) FROM d1l_provider.d1l_bootstrap_consume_intents WHERE authorization_id=$1", authorizationID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func d1lR2ProviderEvidence(t *testing.T, dsn, authorizationID, issuerRequestID, envelope string) (state, consumeID string, intentCount int, ownerIdentity string, issueCount, privilegedReplay int) {
	t.Helper()
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var authJSON string
	if err := db.QueryRowContext(context.Background(), "SELECT state, COALESCE(consume_request_id::text,''), to_jsonb(a)::text FROM d1l_provider.d1l_bootstrap_authorizations a WHERE authorization_id=$1", authorizationID).Scan(&state, &consumeID, &authJSON); err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(envelope, ".")
	if len(parts) < 5 || strings.Contains(authJSON, parts[len(parts)-1]) || strings.Contains(authJSON, envelope) {
		t.Fatal("Provider authorization evidence disclosed raw secret material")
	}
	if err := db.QueryRowContext(context.Background(), "SELECT count(*), COALESCE(max(consumer_identity),'') FROM d1l_provider.d1l_bootstrap_consume_intents WHERE authorization_id=$1", authorizationID).Scan(&intentCount, &ownerIdentity); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(context.Background(), "SELECT count(*) FROM d1l_provider.d1l_issue_requests WHERE issuer_request_id=$1", issuerRequestID).Scan(&issueCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(context.Background(), "SELECT count(*) FROM d1l_provider.d1l_bootstrap_consume_intents WHERE authorization_id=$1 AND consumer_identity <> $2", authorizationID, d1lR2RunnerURI).Scan(&privilegedReplay); err != nil {
		t.Fatal(err)
	}
	return state, consumeID, intentCount, ownerIdentity, issueCount, privilegedReplay
}
