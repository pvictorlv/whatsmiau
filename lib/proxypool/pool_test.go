package proxypool

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/verbeux-ai/whatsmiau/models"
)

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("failed to write %s: %v", path, err)
	}
	return path
}

// newTestPool builds a pool with a clock the test drives, so cooldown and
// hot-reload can be exercised without sleeping.
func newTestPool(t *testing.T, file string, policy Policy, cooldown time.Duration) (*Pool, func(time.Duration)) {
	t.Helper()

	current := time.Unix(1_700_000_000, 0)
	p := &Pool{
		cooldownUntil: map[string]time.Time{},
		assignments:   map[string]models.InstanceProxy{},
		now:           func() time.Time { return current },
		randInt:       func(n int) int { return 0 },
	}
	p.configure(file, policy, cooldown)

	return p, func(d time.Duration) { current = current.Add(d) }
}

func TestParseJSONPoolAcceptsEvolutionFormat(t *testing.T) {
	path := writeFile(t, "proxies.json", `[
		{"host": "186.194.48.234", "port": 30000, "protocol": "http", "username": "evo", "password": "s3cr3t"},
		{"host": "186.194.48.234", "port": "30001", "username": "evo", "password": "s3cr3t"},
		{"host": "", "port": "30002"}
	]`)

	pool, _ := newTestPool(t, path, PolicyRoundRobin, time.Minute)

	status := pool.Status()
	if status.Size != 2 {
		t.Fatalf("expected 2 proxies (third has no host), got %d", status.Size)
	}

	first, err := pool.Acquire("instance-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if first.ProxyPort != "30000" || first.ProxyProtocol != "http" {
		t.Fatalf("unexpected first proxy: %+v", first)
	}

	second, err := pool.Acquire("instance-2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if second.ProxyPort != "30001" {
		t.Fatalf("expected round robin to advance, got %+v", second)
	}
	// A proxy entry without an explicit protocol still has to be usable.
	if second.ProxyProtocol != "http" {
		t.Fatalf("expected protocol to default to http, got %q", second.ProxyProtocol)
	}
}

func TestParseJSONPoolAcceptsWrappedObject(t *testing.T) {
	path := writeFile(t, "proxies.json", `{"proxies": [{"host": "10.0.0.1", "port": 8080}]}`)

	pool, _ := newTestPool(t, path, PolicyRoundRobin, time.Minute)

	if size := pool.Status().Size; size != 1 {
		t.Fatalf("expected 1 proxy, got %d", size)
	}
}

func TestParseLinePoolIgnoresCommentsAndHandlesIPv6(t *testing.T) {
	path := writeFile(t, "proxies.txt", "# comment\n\nhttp://evo:pass@186.194.48.234:30000\n[2804:abcd::1]:3128\nhttp://[unclosed\n")

	pool, _ := newTestPool(t, path, PolicyRoundRobin, time.Minute)

	if size := pool.Status().Size; size != 2 {
		t.Fatalf("expected 2 proxies, got %d", size)
	}

	first, err := pool.Acquire("a")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if first.ProxyHost != "186.194.48.234" || first.ProxyUsername != "evo" || first.ProxyPassword != "pass" {
		t.Fatalf("unexpected proxy: %+v", first)
	}

	second, err := pool.Acquire("b")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if second.ProxyHost != "2804:abcd::1" || second.ProxyPort != "3128" {
		t.Fatalf("expected bracketed IPv6 literal to parse, got %+v", second)
	}
}

func TestAcquireIsStickyPerInstance(t *testing.T) {
	path := writeFile(t, "proxies.json", `[{"host":"h","port":"1"},{"host":"h","port":"2"}]`)
	pool, _ := newTestPool(t, path, PolicyRoundRobin, time.Minute)

	first, _ := pool.Acquire("instance-1")
	again, _ := pool.Acquire("instance-1")
	if first.ProxyPort != again.ProxyPort {
		t.Fatalf("expected the same proxy on reconnect, got %s then %s", first.ProxyPort, again.ProxyPort)
	}
}

func TestMarkFailedRotatesAndCoolsDown(t *testing.T) {
	path := writeFile(t, "proxies.json", `[{"host":"h","port":"1"},{"host":"h","port":"2"}]`)
	pool, advance := newTestPool(t, path, PolicyRoundRobin, 5*time.Minute)

	first, _ := pool.Acquire("instance-1")
	if !pool.MarkFailed("instance-1", "connect refused") {
		t.Fatal("expected MarkFailed to report an assignment")
	}

	next, err := pool.Acquire("instance-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if next.ProxyPort == first.ProxyPort {
		t.Fatalf("expected a different proxy after failure, got %s", next.ProxyPort)
	}

	// While in cooldown the failed proxy must not come back...
	other, _ := pool.Acquire("instance-2")
	if other.ProxyPort == first.ProxyPort {
		t.Fatalf("expected failed proxy %s to stay in cooldown", first.ProxyPort)
	}

	// ...and it must come back once the cooldown expires.
	advance(6 * time.Minute)
	if available := pool.Status().Available; available != 2 {
		t.Fatalf("expected cooldown to expire, available=%d", available)
	}
}

func TestAcquireFailsClosedWhenPoolIsEmpty(t *testing.T) {
	path := writeFile(t, "proxies.json", `[]`)
	pool, _ := newTestPool(t, path, PolicyRoundRobin, time.Minute)

	if _, err := pool.Acquire("instance-1"); err == nil {
		t.Fatal("expected an error so the caller refuses to connect without a proxy")
	}

	if !pool.Configured() {
		t.Fatal("expected the pool to still count as configured")
	}
}

func TestUnconfiguredPoolIsInert(t *testing.T) {
	pool, _ := newTestPool(t, "", PolicyRoundRobin, time.Minute)

	if pool.Configured() {
		t.Fatal("expected an empty file to mean not configured")
	}
	if pool.MarkFailed("instance-1", "whatever") {
		t.Fatal("expected MarkFailed to be a no-op")
	}
}

func TestPoolReloadsWhenFileChanges(t *testing.T) {
	path := writeFile(t, "proxies.json", `[{"host":"h","port":"1"}]`)
	pool, advance := newTestPool(t, path, PolicyRoundRobin, time.Minute)

	assigned, _ := pool.Acquire("instance-1")
	if assigned.ProxyPort != "1" {
		t.Fatalf("unexpected proxy: %+v", assigned)
	}

	if err := os.WriteFile(path, []byte(`[{"host":"h","port":"9"}]`), 0o600); err != nil {
		t.Fatalf("failed to rewrite pool file: %v", err)
	}
	future := time.Now().Add(time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("failed to touch pool file: %v", err)
	}
	advance(reloadThrottle + time.Second)

	// The old assignment no longer exists in the file, so it must be replaced.
	reassigned, err := pool.Acquire("instance-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reassigned.ProxyPort != "9" {
		t.Fatalf("expected the pool to reload from disk, got %+v", reassigned)
	}
}

func TestURL(t *testing.T) {
	tests := []struct {
		name  string
		proxy models.InstanceProxy
		want  string
	}{
		{
			name:  "with credentials",
			proxy: models.InstanceProxy{ProxyHost: "1.2.3.4", ProxyPort: "30000", ProxyProtocol: "HTTP", ProxyUsername: "evo", ProxyPassword: "p@ss"},
			want:  "http://evo:p%40ss@1.2.3.4:30000",
		},
		{
			name:  "without credentials",
			proxy: models.InstanceProxy{ProxyHost: "1.2.3.4", ProxyPort: "3128"},
			want:  "http://1.2.3.4:3128",
		},
		{
			name:  "ipv6 literal",
			proxy: models.InstanceProxy{ProxyHost: "2804:abcd::1", ProxyPort: "3128", ProxyProtocol: "socks5"},
			want:  "socks5://[2804:abcd::1]:3128",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := URL(test.proxy); got != test.want {
				t.Fatalf("expected %q, got %q", test.want, got)
			}
		})
	}
}
