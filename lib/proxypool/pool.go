// Package proxypool serves a rotating pool of outbound proxies read from a
// file on disk.
//
// It exists so a fleet of WhatsApp sessions can spread its traffic over many
// exit IPs (typically one IPv6 per port on a 3proxy VPS) without every instance
// carrying its own proxy credentials: the operator points PROXY_POOL_FILE at
// the same file the Evolution API reads and each instance gets one proxy from
// the pool, with failing proxies quarantined for a cooldown period.
//
// The file is re-read whenever its modification time changes, so adding or
// removing proxies never requires a restart.
package proxypool

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/verbeux-ai/whatsmiau/models"
	"go.uber.org/zap"
)

// Policy decides which proxy Acquire hands out to an instance that does not
// have one yet.
type Policy string

const (
	PolicyRoundRobin Policy = "round_robin"
	PolicyRandom     Policy = "random"
	PolicySticky     Policy = "sticky"
)

const (
	reloadThrottle  = 2 * time.Second
	defaultCooldown = 5 * time.Minute
)

// ErrNoProxyAvailable is returned by Acquire when a pool is configured but has
// nothing healthy to hand out. Callers must refuse to connect in that case:
// connecting directly would expose the server IP to WhatsApp, which is the
// exact thing the pool exists to prevent.
var ErrNoProxyAvailable = errors.New("proxy pool is configured but no proxy is available")

// Status is a snapshot of the pool for logging and health endpoints.
type Status struct {
	Configured bool   `json:"configured"`
	Size       int    `json:"size"`
	Available  int    `json:"available"`
	Assigned   int    `json:"assigned"`
	Policy     Policy `json:"policy"`
	File       string `json:"file"`
}

// Pool is a file-backed set of proxies with per-instance assignment.
type Pool struct {
	mu       sync.Mutex
	file     string
	policy   Policy
	cooldown time.Duration

	entries       []models.InstanceProxy
	index         int
	cooldownUntil map[string]time.Time
	assignments   map[string]models.InstanceProxy

	lastMtime time.Time
	lastCheck time.Time

	now     func() time.Time
	randInt func(n int) int
}

// New builds a pool reading from file. An empty file means "not configured":
// every call is then a no-op and instances connect with whatever proxy they
// carry themselves.
func New(file string, policy Policy, cooldown time.Duration) *Pool {
	p := &Pool{
		cooldownUntil: map[string]time.Time{},
		assignments:   map[string]models.InstanceProxy{},
		now:           time.Now,
		randInt:       rand.IntN,
	}
	p.configure(file, policy, cooldown)
	return p
}

func (p *Pool) configure(file string, policy Policy, cooldown time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.file = strings.TrimSpace(file)
	p.policy = normalizePolicy(policy)
	p.cooldown = cooldown
	if p.cooldown <= 0 {
		p.cooldown = defaultCooldown
	}
	p.entries = nil
	p.index = -1
	p.lastMtime = time.Time{}
	p.cooldownUntil = map[string]time.Time{}
	p.assignments = map[string]models.InstanceProxy{}
	p.reloadLocked(true)
}

// Configured tells whether a pool file was set at all.
func (p *Pool) Configured() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.file != ""
}

// Acquire returns the proxy assigned to instanceID, picking a new one when the
// instance has none, when its proxy left the file, or when its proxy is in
// cooldown. The assignment is sticky in memory so reconnects keep the same exit
// IP — WhatsApp treats a session that hops IPs on every reconnect as suspicious.
func (p *Pool) Acquire(instanceID string) (models.InstanceProxy, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.file == "" {
		return models.InstanceProxy{}, ErrNoProxyAvailable
	}

	p.reloadLocked(false)

	if assigned, ok := p.assignments[instanceID]; ok && p.knownLocked(assigned) && p.liveLocked(assigned) {
		return assigned, nil
	}

	picked, ok := p.pickLocked()
	if !ok {
		delete(p.assignments, instanceID)
		return models.InstanceProxy{}, ErrNoProxyAvailable
	}

	p.assignments[instanceID] = picked
	zap.L().Info("proxy pool assignment",
		zap.String("instance", instanceID),
		zap.String("proxy", picked.ProxyHost+":"+picked.ProxyPort),
		zap.String("policy", string(p.policy)))

	return picked, nil
}

// MarkFailed quarantines the proxy currently assigned to instanceID and drops
// the assignment, so the next Acquire hands out a different one. It reports
// whether there was an assignment to punish.
func (p *Pool) MarkFailed(instanceID, reason string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	assigned, ok := p.assignments[instanceID]
	if !ok {
		return false
	}

	delete(p.assignments, instanceID)
	p.cooldownUntil[keyOf(assigned)] = p.now().Add(p.cooldown)
	zap.L().Warn("proxy marked as failed",
		zap.String("instance", instanceID),
		zap.String("proxy", assigned.ProxyHost+":"+assigned.ProxyPort),
		zap.String("reason", reason),
		zap.Duration("cooldown", p.cooldown))

	return true
}

// Release forgets the assignment of an instance that no longer exists.
func (p *Pool) Release(instanceID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.assignments, instanceID)
}

// Status snapshots the pool.
func (p *Pool) Status() Status {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.file != "" {
		p.reloadLocked(false)
	}

	return Status{
		Configured: p.file != "",
		Size:       len(p.entries),
		Available:  len(p.liveEntriesLocked()),
		Assigned:   len(p.assignments),
		Policy:     p.policy,
		File:       p.file,
	}
}

func (p *Pool) pickLocked() (models.InstanceProxy, bool) {
	if len(p.entries) == 0 {
		return models.InstanceProxy{}, false
	}

	candidates := p.liveEntriesLocked()
	if len(candidates) == 0 {
		// Everything is in cooldown: a pool with no usable proxy is worse than
		// a possibly-recovered one, so give them all another chance.
		zap.L().Warn("all proxies in cooldown; clearing cooldowns and retrying")
		p.cooldownUntil = map[string]time.Time{}
		candidates = p.liveEntriesLocked()
	}
	if len(candidates) == 0 {
		return models.InstanceProxy{}, false
	}

	switch p.policy {
	case PolicySticky:
		return candidates[0], true
	case PolicyRandom:
		return candidates[p.randInt(len(candidates))], true
	default:
		for step := 1; step <= len(p.entries); step++ {
			idx := (p.index + step) % len(p.entries)
			if p.liveLocked(p.entries[idx]) {
				p.index = idx
				return p.entries[idx], true
			}
		}
		return models.InstanceProxy{}, false
	}
}

func (p *Pool) liveEntriesLocked() []models.InstanceProxy {
	live := make([]models.InstanceProxy, 0, len(p.entries))
	for _, entry := range p.entries {
		if p.liveLocked(entry) {
			live = append(live, entry)
		}
	}
	return live
}

func (p *Pool) liveLocked(proxy models.InstanceProxy) bool {
	until, ok := p.cooldownUntil[keyOf(proxy)]
	return !ok || !until.After(p.now())
}

func (p *Pool) knownLocked(proxy models.InstanceProxy) bool {
	target := keyOf(proxy)
	for _, entry := range p.entries {
		if keyOf(entry) == target {
			return true
		}
	}
	return false
}

func (p *Pool) reloadLocked(force bool) {
	if p.file == "" {
		p.entries = nil
		return
	}

	now := p.now()
	if !force && now.Sub(p.lastCheck) < reloadThrottle {
		return
	}
	p.lastCheck = now

	info, err := os.Stat(p.file)
	if err != nil {
		if len(p.entries) > 0 {
			zap.L().Error("proxy pool file is unreadable — pool is now empty",
				zap.String("file", p.file), zap.Error(err))
		}
		p.entries = nil
		p.lastMtime = time.Time{}
		return
	}

	if !force && info.ModTime().Equal(p.lastMtime) {
		return
	}

	raw, err := os.ReadFile(p.file)
	if err != nil {
		zap.L().Error("failed to read proxy pool file", zap.String("file", p.file), zap.Error(err))
		return
	}

	parsed := parseFile(p.file, raw)
	p.lastMtime = info.ModTime()
	if len(parsed) == 0 {
		zap.L().Error("proxy pool file produced 0 valid proxies", zap.String("file", p.file))
		p.entries = nil
		return
	}

	p.entries = parsed
	if p.index >= len(p.entries) {
		p.index = -1
	}
	zap.L().Info("proxy pool loaded",
		zap.String("file", p.file),
		zap.Int("size", len(parsed)),
		zap.String("policy", string(p.policy)))
}

// fileEntry mirrors the Evolution API proxies.json shape. Port accepts both
// 30000 and "30000" because both spellings exist in the wild.
type fileEntry struct {
	Host     string      `json:"host"`
	Port     json.Number `json:"port"`
	Protocol string      `json:"protocol"`
	Username string      `json:"username"`
	Password string      `json:"password"`
}

func parseFile(path string, raw []byte) []models.InstanceProxy {
	if strings.HasSuffix(strings.ToLower(strings.TrimSpace(path)), ".json") {
		return parseJSON(raw)
	}
	return parseLines(raw)
}

func parseJSON(raw []byte) []models.InstanceProxy {
	var list []fileEntry
	if err := json.Unmarshal(raw, &list); err != nil {
		var wrapper struct {
			Proxies []fileEntry `json:"proxies"`
		}
		if wrapErr := json.Unmarshal(raw, &wrapper); wrapErr != nil {
			zap.L().Error("failed to parse proxy pool json", zap.Error(err))
			return nil
		}
		list = wrapper.Proxies
	}

	proxies := make([]models.InstanceProxy, 0, len(list))
	for _, entry := range list {
		host := strings.TrimSpace(entry.Host)
		port := strings.TrimSpace(entry.Port.String())
		if host == "" || port == "" {
			zap.L().Warn("ignoring proxy pool entry without host/port", zap.Any("entry", entry))
			continue
		}

		protocol := strings.ToLower(strings.TrimSpace(entry.Protocol))
		if protocol == "" {
			protocol = "http"
		}

		proxies = append(proxies, models.InstanceProxy{
			ProxyHost:     strings.Trim(host, "[]"),
			ProxyPort:     port,
			ProxyProtocol: protocol,
			ProxyUsername: entry.Username,
			ProxyPassword: entry.Password,
		})
	}

	return proxies
}

func parseLines(raw []byte) []models.InstanceProxy {
	lines := strings.Split(string(raw), "\n")
	proxies := make([]models.InstanceProxy, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		proxy, err := ParseURL(line)
		if err != nil {
			zap.L().Warn("ignoring malformed proxy line", zap.String("line", line), zap.Error(err))
			continue
		}
		proxies = append(proxies, proxy)
	}

	return proxies
}

// ParseURL turns "<scheme>://[user:pass@]host:port" into an InstanceProxy. The
// scheme is optional and defaults to http, and IPv6 literals may be bracketed.
func ParseURL(raw string) (models.InstanceProxy, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return models.InstanceProxy{}, errors.New("empty proxy url")
	}
	if !strings.Contains(value, "://") {
		value = "http://" + value
	}

	parsed, err := url.Parse(value)
	if err != nil {
		return models.InstanceProxy{}, fmt.Errorf("failed to parse proxy url %q: %w", raw, err)
	}

	host := parsed.Hostname()
	if host == "" {
		return models.InstanceProxy{}, fmt.Errorf("proxy url %q has no host", raw)
	}

	port := parsed.Port()
	if port == "" {
		port = defaultPort(parsed.Scheme)
	}

	var username, password string
	if parsed.User != nil {
		username = parsed.User.Username()
		password, _ = parsed.User.Password()
	}

	return models.InstanceProxy{
		ProxyHost:     host,
		ProxyPort:     port,
		ProxyProtocol: strings.ToLower(parsed.Scheme),
		ProxyUsername: username,
		ProxyPassword: password,
	}, nil
}

// URL renders a proxy as the address whatsmeow expects: IPv6 literals get
// bracketed and credentials get escaped, and a proxy without credentials is
// rendered without a userinfo part.
func URL(proxy models.InstanceProxy) string {
	protocol := strings.ToLower(strings.TrimSpace(proxy.ProxyProtocol))
	if protocol == "" {
		protocol = "http"
	}

	host := strings.TrimSpace(proxy.ProxyHost)
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	if port := strings.TrimSpace(proxy.ProxyPort); port != "" {
		host += ":" + port
	}

	switch {
	case proxy.ProxyUsername == "" && proxy.ProxyPassword == "":
		return protocol + "://" + host
	case proxy.ProxyPassword == "":
		return protocol + "://" + url.User(proxy.ProxyUsername).String() + "@" + host
	default:
		return protocol + "://" + url.UserPassword(proxy.ProxyUsername, proxy.ProxyPassword).String() + "@" + host
	}
}

func defaultPort(scheme string) string {
	switch strings.ToLower(scheme) {
	case "https":
		return "443"
	case "socks5", "socks5h":
		return "1080"
	default:
		return "80"
	}
}

func normalizePolicy(policy Policy) Policy {
	switch Policy(strings.ToLower(strings.TrimSpace(string(policy)))) {
	case PolicyRandom:
		return PolicyRandom
	case PolicySticky:
		return PolicySticky
	default:
		return PolicyRoundRobin
	}
}

func keyOf(proxy models.InstanceProxy) string {
	return fmt.Sprintf("%s://%s@%s:%s",
		strings.ToLower(proxy.ProxyProtocol), proxy.ProxyUsername, proxy.ProxyHost, proxy.ProxyPort)
}

var defaultPool = New("", PolicyRoundRobin, 0)

// Configure points the process-wide pool at a file. Call it once at startup,
// after the logger is up so the load result is visible.
func Configure(file string, policy Policy, cooldown time.Duration) {
	defaultPool.configure(file, policy, cooldown)
}

// Configured reports whether a pool file was set.
func Configured() bool { return defaultPool.Configured() }

// Acquire returns the pool proxy assigned to an instance.
func Acquire(instanceID string) (models.InstanceProxy, error) { return defaultPool.Acquire(instanceID) }

// MarkFailed quarantines the proxy assigned to an instance.
func MarkFailed(instanceID, reason string) bool { return defaultPool.MarkFailed(instanceID, reason) }

// Release forgets an instance's assignment.
func Release(instanceID string) { defaultPool.Release(instanceID) }

// Snapshot describes the process-wide pool.
func Snapshot() Status { return defaultPool.Status() }
