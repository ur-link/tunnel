package server

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/registration"

	"github.com/go-acme/lego/v4/providers/dns/cloudflare"
	"github.com/go-acme/lego/v4/providers/dns/digitalocean"
	"github.com/go-acme/lego/v4/providers/dns/gcloud"
	"github.com/go-acme/lego/v4/providers/dns/route53"

	"github.com/ur-link/tunnel/internal/config"
)

// newDNSProvider returns a curated DNS-01 provider by name. Each reads its
// credentials from the provider's standard env vars (see go-acme/lego docs).
// Kept curated (not lego's all-providers aggregator) so the binary stays lean;
// add a case + import to support another provider.
func newDNSProvider(name string) (challenge.Provider, error) {
	switch strings.ToLower(name) {
	case "cloudflare": // CF_DNS_API_TOKEN (or CF_API_EMAIL + CF_API_KEY)
		return cloudflare.NewDNSProvider()
	case "route53": // AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY / AWS_REGION
		return route53.NewDNSProvider()
	case "digitalocean": // DO_AUTH_TOKEN
		return digitalocean.NewDNSProvider()
	case "gcloud": // GCE_PROJECT + application default credentials
		return gcloud.NewDNSProvider()
	default:
		return nil, fmt.Errorf("unsupported dns provider %q (built-in: cloudflare, route53, digitalocean, gcloud)", name)
	}
}

// acmeUser implements lego's registration.User.
type acmeUser struct {
	Email string
	Reg   *registration.Resource
	key   crypto.PrivateKey
}

func (u *acmeUser) GetEmail() string                        { return u.Email }
func (u *acmeUser) GetRegistration() *registration.Resource { return u.Reg }
func (u *acmeUser) GetPrivateKey() crypto.PrivateKey        { return u.key }

// dnsCertMgr issues wildcard certificates via ACME DNS-01 (lego) and serves them
// over SNI. With nested subdomains it issues a "*.<namespace>.<domain>" cert per
// namespace on demand; otherwise a single "*.<domain>" covers every tunnel.
// Certs are cached in memory and on disk and renewed when near expiry.
type dnsCertMgr struct {
	domain   string
	nested   bool
	cacheDir string
	client   *lego.Client
	log      *slog.Logger

	mu    sync.Mutex
	certs map[string]*tls.Certificate
}

func newDNSCertMgr(cfg *config.Server, log *slog.Logger) (*dnsCertMgr, error) {
	m := &dnsCertMgr{domain: cfg.Domain, nested: cfg.NestedSubdomains, cacheDir: cfg.TLSCacheDir, log: log, certs: map[string]*tls.Certificate{}}
	if err := os.MkdirAll(m.cacheDir, 0o700); err != nil {
		return nil, err
	}
	user, err := m.loadOrCreateUser(cfg.TLSACMEEmail)
	if err != nil {
		return nil, err
	}
	lcfg := lego.NewConfig(user)
	lcfg.Certificate.KeyType = certcrypto.EC256
	client, err := lego.NewClient(lcfg)
	if err != nil {
		return nil, err
	}
	prov, err := newDNSProvider(cfg.TLSDNSProvider)
	if err != nil {
		return nil, fmt.Errorf("dns provider %q: %w (set its credential env vars, see go-acme/lego docs)", cfg.TLSDNSProvider, err)
	}
	if err := client.Challenge.SetDNS01Provider(prov); err != nil {
		return nil, err
	}
	if user.Reg == nil {
		reg, err := client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
		if err != nil {
			return nil, fmt.Errorf("acme account registration: %w", err)
		}
		user.Reg = reg
		_ = m.saveUserReg(reg)
	}
	m.client = client
	return m, nil
}

// TLSConfig returns a config that obtains/serves wildcard certs on demand.
func (m *dnsCertMgr) TLSConfig() *tls.Config {
	return &tls.Config{GetCertificate: m.getCertificate, MinVersion: tls.VersionTLS12}
}

func (m *dnsCertMgr) getCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	name := m.wildcardFor(hello.ServerName)
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.certs[name]; ok && !expiringSoon(c) {
		return c, nil
	}
	if c := m.loadCert(name); c != nil && !expiringSoon(c) {
		m.certs[name] = c
		return c, nil
	}
	c, err := m.obtain(name)
	if err != nil {
		if cached := m.certs[name]; cached != nil {
			m.log.Warn("acme: serving cached cert after renewal failure", "cert", name, "err", err)
			return cached, nil
		}
		return nil, err
	}
	m.certs[name] = c
	return c, nil
}

// wildcardFor maps an SNI server name to the wildcard cert that should cover it.
func (m *dnsCertMgr) wildcardFor(server string) string {
	server = strings.ToLower(strings.TrimSuffix(server, "."))
	sub := strings.TrimSuffix(server, "."+m.domain)
	if m.nested && sub != server && strings.Contains(sub, ".") {
		parts := strings.Split(sub, ".")
		ns := parts[len(parts)-1] // <slug>.<namespace> -> namespace
		return "*." + ns + "." + m.domain
	}
	return "*." + m.domain
}

func (m *dnsCertMgr) obtain(wildcard string) (*tls.Certificate, error) {
	base := strings.TrimPrefix(wildcard, "*.")
	m.log.Info("acme: obtaining wildcard certificate via DNS-01", "cert", wildcard)
	res, err := m.client.Certificate.Obtain(certificate.ObtainRequest{
		Domains: []string{wildcard, base}, Bundle: true,
	})
	if err != nil {
		return nil, fmt.Errorf("obtain %s: %w", wildcard, err)
	}
	cert, err := tls.X509KeyPair(res.Certificate, res.PrivateKey)
	if err != nil {
		return nil, err
	}
	m.saveCert(wildcard, res.Certificate, res.PrivateKey)
	return &cert, nil
}

// --- persistence ---

func (m *dnsCertMgr) loadOrCreateUser(email string) (*acmeUser, error) {
	keyPath := filepath.Join(m.cacheDir, "acme-account.key")
	if b, err := os.ReadFile(keyPath); err == nil {
		block, _ := pem.Decode(b)
		if block != nil {
			if key, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
				u := &acmeUser{Email: email, key: key}
				if rb, err := os.ReadFile(filepath.Join(m.cacheDir, "acme-account.json")); err == nil {
					_ = json.Unmarshal(rb, &u.Reg)
				}
				return u, nil
			}
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	_ = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600)
	return &acmeUser{Email: email, key: key}, nil
}

func (m *dnsCertMgr) saveUserReg(reg *registration.Resource) error {
	b, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(m.cacheDir, "acme-account.json"), b, 0o600)
}

func (m *dnsCertMgr) certPaths(wildcard string) (crt, key string) {
	safe := strings.ReplaceAll(wildcard, "*", "_wildcard_")
	return filepath.Join(m.cacheDir, safe+".crt"), filepath.Join(m.cacheDir, safe+".key")
}

func (m *dnsCertMgr) saveCert(wildcard string, crtPEM, keyPEM []byte) {
	crt, key := m.certPaths(wildcard)
	_ = os.WriteFile(crt, crtPEM, 0o600)
	_ = os.WriteFile(key, keyPEM, 0o600)
}

func (m *dnsCertMgr) loadCert(wildcard string) *tls.Certificate {
	crt, key := m.certPaths(wildcard)
	c, err := tls.LoadX509KeyPair(crt, key)
	if err != nil {
		return nil
	}
	return &c
}

// expiringSoon reports whether a cert is within 30 days of expiry (renew window).
func expiringSoon(c *tls.Certificate) bool {
	leaf := c.Leaf
	if leaf == nil {
		if len(c.Certificate) == 0 {
			return true
		}
		parsed, err := x509.ParseCertificate(c.Certificate[0])
		if err != nil {
			return true
		}
		leaf = parsed
	}
	return time.Until(leaf.NotAfter) < 30*24*time.Hour
}
