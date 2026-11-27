package cli

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestRmWithoutReading(t *testing.T) {
	var mu sync.Mutex
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.Method
		if r.Method == "GET" && r.URL.Query().Get("list") == "true" {
			method = "LIST"
		}
		mu.Lock()
		requests = append(requests, method+" "+r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/sys/internal/ui/mounts/"):
			fmt.Fprint(w, `{"data":{"path":"secret/","type":"kv","options":{"version":"2"}}}`)
		case method == "LIST" && r.URL.Path == "/v1/secret/metadata":
			fmt.Fprint(w, `{"data":{"keys":["bad","team/"]}}`)
		case method == "LIST" && r.URL.Path == "/v1/secret/metadata/team":
			fmt.Fprint(w, `{"data":{"keys":["good","bad"]}}`)
		case r.Method == "DELETE" && r.URL.Path == "/v1/secret/data/team/bad":
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"errors":["delete failed"]}`)
		case r.Method == "DELETE":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == "PUT" && r.URL.Path == "/v1/secret/destroy/bad":
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"errors":["could not find version data"]}`)
		}
	}))
	defer server.Close()
	t.Setenv("VAULT_ADDR", server.URL)
	t.Setenv("VAULT_TOKEN", "test-token")
	t.Setenv("VAULT_MAX_RETRIES", "0")
	t.Setenv("NO_COLOR", "1")

	run := func(args ...string) (string, string, error, []string) {
		t.Helper()
		mu.Lock()
		requests = nil
		mu.Unlock()
		var out, errOut bytes.Buffer
		root := NewRoot()
		root.SetArgs(args)
		root.SetOut(&out)
		root.SetErr(&errOut)
		root.SetIn(strings.NewReader("yes\n"))
		err := root.Execute()
		mu.Lock()
		defer mu.Unlock()
		return out.String(), errOut.String(), err, append([]string(nil), requests...)
	}

	_, _, err, _ := run("rm", "secret/bad", "--yes")
	if err == nil || !strings.Contains(err.Error(), "could not find version data") {
		t.Fatalf("normal planning must report read failure: %v", err)
	}

	out, _, err, reqs := run("rm", "-r", "secret/team/", "--no-read", "--no-backup", "--dry-run")
	if err != nil || !strings.Contains(out, "secret/team/bad") || !strings.Contains(out, "secret/team/good") || !strings.Contains(out, "state not read; no backup") {
		t.Fatalf("dry run: %v, output %q", err, out)
	}
	for _, req := range reqs {
		if strings.HasPrefix(req, "DELETE ") || strings.HasPrefix(req, "GET /v1/secret/") {
			t.Errorf("unexpected request during dry run: %s", req)
		}
	}

	out, _, err, reqs = run("rm", "-r", "secret/team/", "--no-read", "--no-backup", "--include", "good", "--dry-run")
	if err != nil || strings.Contains(out, "secret/team/bad") || !strings.Contains(out, "secret/team/good") {
		t.Fatalf("filtered plan: %v, output %q, requests %v", err, out, reqs)
	}
	_, _, err, reqs = run("rm", "-r", "secret/team/", "--no-read", "--no-backup", "--include", "missing", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "nothing found") {
		t.Fatalf("empty filtered plan: %v, requests %v", err, reqs)
	}
	for _, req := range reqs {
		if strings.HasPrefix(req, "GET /v1/secret/") {
			t.Errorf("empty filtered plan read state: %s", req)
		}
	}

	_, _, err, reqs = run("rm", "secret/bad", "--no-read", "--no-backup", "--yes")
	if err != nil || !containsRequest(reqs, "DELETE /v1/secret/data/bad") {
		t.Fatalf("single secret: %v, requests %v", err, reqs)
	}
	_, _, err, reqs = run("rm", "-r", "secret/team", "--wipe", "--no-read", "--no-backup", "--yes")
	if err != nil || !containsRequest(reqs, "DELETE /v1/secret/metadata/team/bad") || !containsRequest(reqs, "DELETE /v1/secret/metadata/team/good") {
		t.Fatalf("wipe: %v, requests %v", err, reqs)
	}
	_, errOut, err, reqs := run("rm", "secret/bad", "--destroy", "--versions", "1", "--no-read", "--no-backup")
	if err != nil || !strings.Contains(errOut, "Type yes to apply") || !strings.Contains(errOut, "no kvx backup") || !containsRequest(reqs, "PUT /v1/secret/destroy/bad") {
		t.Fatalf("destroy confirmation: %v, stderr %q, requests %v", err, errOut, reqs)
	}
	_, errOut, err, _ = run("rm", "-r", "secret/team/", "--no-read", "--no-backup", "--yes")
	if err == nil || !strings.Contains(err.Error(), "delete failed") || strings.Contains(errOut, "kvx undo") {
		t.Fatalf("delete failure must be reported without suggesting a missing backup: %v, stderr %q", err, errOut)
	}

	for _, args := range [][]string{
		{"--no-read"},
		{"--no-read", "--no-backup", "--only-empty"},
		{"--no-read", "--no-backup", "--older-than", "30d"},
		{"--no-read", "--no-backup", "--destroy"},
	} {
		_, _, err, reqs := run(append([]string{"rm", "secret/bad"}, args...)...)
		if err == nil || len(reqs) != 0 {
			t.Errorf("invalid flags %v: err=%v requests=%v", args, err, reqs)
		}
	}
}

func containsRequest(requests []string, want string) bool {
	for _, r := range requests {
		if r == want {
			return true
		}
	}
	return false
}
