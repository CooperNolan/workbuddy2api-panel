package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/misakano7545/workbuddy2api-panel/internal/auth"
	"github.com/misakano7545/workbuddy2api-panel/internal/pool"
	"github.com/misakano7545/workbuddy2api-panel/internal/proxypool"
)

func TestProxyAPIAuthenticationBindingAndRemoval(t *testing.T) {
	manager, err := proxypool.New(filepath.Join(t.TempDir(), "proxy-pool.json"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	accounts := pool.New("")
	accounts.Add(&auth.Auth{UID: "u1", AccessToken: "token"})
	p := New(Config{Pool: accounts, ProxyPool: manager, APIKey: "panel-key"})
	call := func(method, path, body, key string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "/panel/api/"+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+key)
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, req)
		return rec
	}
	for _, route := range []struct{ method, path string }{
		{"GET", "proxies"}, {"POST", "proxies"}, {"PATCH", "proxies/missing"},
		{"DELETE", "proxies/missing"}, {"POST", "proxies/check"}, {"POST", "proxies/missing/check"},
		{"PUT", "proxies/health"}, {"PUT", "accounts/u1/proxy"}, {"PUT", "proxies/realm/cn"},
		{"POST", "accounts/assign-proxies"},
	} {
		if got := call(route.method, route.path, "{}", "wrong"); got.Code != http.StatusUnauthorized {
			t.Fatalf("unauthorized %s %s: %d", route.method, route.path, got.Code)
		}
	}
	imported := call("POST", "proxies", `{"lines":"http://user:TOPSECRET@localhost:8080\nbad://host\nhttp://user:TOPSECRET@localhost:8080"}`, "panel-key")
	if imported.Code != 200 || strings.Contains(imported.Body.String(), "TOPSECRET") {
		t.Fatalf("import: %d %s", imported.Code, imported.Body.String())
	}
	var data struct {
		Results []proxypool.ImportResult `json:"results"`
	}
	if err := json.Unmarshal(imported.Body.Bytes(), &data); err != nil || len(data.Results) != 3 {
		t.Fatalf("results=%+v err=%v", data, err)
	}
	id := data.Results[0].ID
	for _, route := range []struct {
		method, path, body string
		want               int
	}{
		{"GET", "proxies", "", 200},
		{"PATCH", "proxies/" + id, `{"name":"Office","password":""}`, 200},
		{"PUT", "accounts/u1/proxy", `{"mode":"fixed","proxy_id":"` + id + `"}`, 200},
		{"DELETE", "proxies/" + id, "", 409},
		{"POST", "proxies/" + id + "/check", "", 202},
		{"POST", "proxies/check", "", 202},
		{"POST", "proxies/missing/check", "", 404},
		{"POST", "proxies", `{"lines":"http://localhost:1234"} {}`, 400},
		{"PUT", "accounts/missing/proxy", `{"mode":"direct"}`, 404},
		{"PUT", "accounts/u1/proxy", `{"mode":"fixed","proxy_id":"missing"}`, 404},
		{"PUT", "accounts/u1/proxy", `{"mode":"bogus"}`, 400},
		{"PUT", "proxies/health", `{"url":"file:///tmp/check","interval_seconds":60}`, 400},
		// realm 默认出口（issue #39 P3）：只认 cn/global，绑不存在的代理 404。
		{"PUT", "proxies/realm/cn", `{"mode":"fixed","proxy_id":"` + id + `"}`, 200},
		{"PUT", "proxies/realm/global", `{"mode":"direct"}`, 200},
		{"PUT", "proxies/realm/us", `{"mode":"direct"}`, 400},
		{"PUT", "proxies/realm/global", `{"mode":"fixed","proxy_id":"missing"}`, 404},
		// 绑了 realm 默认的代理不许直接删（删掉等于整域静默掉回随机池）。
		{"DELETE", "proxies/" + id, "", 409},
		{"PUT", "proxies/realm/cn", `{"mode":"random"}`, 200},
		{"POST", "accounts/u1/remove", "", 200},
		{"DELETE", "proxies/" + id, "", 200},
	} {
		got := call(route.method, route.path, route.body, "panel-key")
		if got.Code != route.want || strings.Contains(got.Body.String(), "TOPSECRET") {
			t.Fatalf("%s %s: %d %s; want %d", route.method, route.path, got.Code, got.Body.String(), route.want)
		}
	}
	if len(manager.Snapshot().Bindings) != 0 {
		t.Fatal("account removal retained binding")
	}
}

func TestAssignAccountProxiesBalancesExistingBindings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy-pool.json")
	manager, err := proxypool.New(path, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	proxy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	a, b := httptest.NewServer(proxy), httptest.NewServer(proxy)
	defer a.Close()
	defer b.Close()
	results, err := manager.Import(strings.Join([]string{a.URL, b.URL, "http://127.0.0.1:1"}, "\n"))
	if err != nil || len(results) != 3 {
		t.Fatalf("proxy import: %+v, %v", results, err)
	}
	settings := proxypool.DefaultSettings()
	settings.URL = "http://health.invalid/"
	if err := manager.Configure(settings); err != nil {
		t.Fatal(err)
	}
	fixed := proxypool.Binding{Mode: "fixed", ProxyID: results[0].ID}
	accounts := pool.New("")
	for i := range 20 {
		uid := fmt.Sprintf("u%02d", i)
		accounts.Add(&auth.Auth{UID: uid})
		if i < 5 {
			if err := manager.Bind(uid, fixed); err != nil {
				t.Fatal(err)
			}
		}
	}
	accounts.Add(&auth.Auth{UID: "direct"})
	if err := manager.Bind("direct", proxypool.Binding{Mode: "direct"}); err != nil {
		t.Fatal(err)
	}
	p := New(Config{Pool: accounts, ProxyPool: manager})
	assign := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, httptest.NewRequest("POST", "/panel/api/accounts/assign-proxies", nil))
		return rec
	}
	if rec := assign(); rec.Code != http.StatusConflict || len(manager.Snapshot().Bindings) != 6 {
		t.Fatalf("no healthy proxy: %d %s", rec.Code, rec.Body.String())
	}
	for _, r := range results[:2] {
		if err := manager.Probe(context.Background(), r.ID); err != nil {
			t.Fatal(err)
		}
	}
	if rec := assign(); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"assigned":15`) {
		t.Fatalf("assign: %d %s", rec.Code, rec.Body.String())
	}
	reopened, err := proxypool.New(path, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	snapshot := reopened.Snapshot()
	if snapshot.Proxies[0].BoundCount != 10 || snapshot.Proxies[1].BoundCount != 10 ||
		snapshot.Proxies[2].BoundCount != 0 || snapshot.Bindings["direct"].Mode != "direct" {
		t.Fatalf("unexpected persisted distribution: %+v", snapshot)
	}
	for i := range 5 {
		if snapshot.Bindings[fmt.Sprintf("u%02d", i)] != fixed {
			t.Fatal("an existing binding changed")
		}
	}
	if rec := assign(); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"assigned":0`) {
		t.Fatalf("repeat assign: %d %s", rec.Code, rec.Body.String())
	}

	if err := manager.BindRealm("global", fixed); err != nil {
		t.Fatal(err)
	}
	inherited := &auth.Auth{UID: "inherited"}
	auth.BackfillRealmFor(inherited, "global")
	accounts.Add(inherited)
	accounts.Add(&auth.Auth{UID: "new"})
	if rec := assign(); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"assigned":1`) {
		t.Fatalf("assign with realm default: %d %s", rec.Code, rec.Body.String())
	}
	bindings := manager.Snapshot().Bindings
	if _, assigned := bindings["inherited"]; assigned || bindings["new"].ProxyID != results[1].ID {
		t.Fatalf("realm default should stay in use and count toward its proxy load: %+v", bindings)
	}
}

func TestProxyAPIRejectsOversizedBody(t *testing.T) {
	m, err := proxypool.New("", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	p := New(Config{Pool: pool.New(""), ProxyPool: m})
	body := `{"lines":"` + strings.Repeat("x", 1<<20) + `"}`
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("POST", "/panel/api/proxies", strings.NewReader(body)))
	if rec.Code != http.StatusBadRequest || len(m.Snapshot().Proxies) != 0 {
		t.Fatalf("oversized request: %d", rec.Code)
	}
}
