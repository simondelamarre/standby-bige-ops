// Package web serves the waiting page on the app's domain and the health
// endpoint the load balancer probes.
package web

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/simondelamarre/standby-bige-ops/internal/config"
	"github.com/simondelamarre/standby-bige-ops/internal/controller"
)

//go:embed assets/page.html assets/app.js
var assets embed.FS

const (
	maxLogo    = 64 << 10
	tokenTTL   = 15 * time.Minute
	pageCSP    = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"
	sandboxCSP = "default-src 'none'; style-src 'unsafe-inline'; sandbox"
)

// Controller is what the web layer needs from the controller.
type Controller interface {
	Status() controller.Status
	RequestWake(reason string) bool
	Healthy() bool
}

type Server struct {
	ctrl   Controller
	page   config.Page
	setup  []string
	wake   config.Wake
	text   texts
	tmpl   *template.Template
	js     []byte
	css    []byte
	logo   []byte
	logoCT string
	key    []byte
	Now    func() time.Time
}

// New prepares the page. logoDir resolves a relative page.logo path.
func New(ctrl Controller, cfg *config.Config, logoDir string) (*Server, error) {
	tmpl, err := template.ParseFS(assets, "assets/page.html")
	if err != nil {
		return nil, err
	}
	js, err := assets.ReadFile("assets/app.js")
	if err != nil {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	s := &Server{
		ctrl: ctrl, page: cfg.Page, wake: cfg.Wake, text: textsFor(cfg.Page.Lang),
		tmpl: tmpl, js: js, css: stylesheet(cfg.Page), key: key, Now: time.Now,
	}
	if cfg.Page.ShowSetup {
		s.setup = setupLines(cfg, s.text)
	}
	if cfg.Page.Logo != "" {
		path := cfg.Page.Logo
		if !strings.HasPrefix(path, "/") {
			path = logoDir + "/" + path
		}
		if s.logo, s.logoCT, err = loadLogo(path); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func loadLogo(path string) ([]byte, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxLogo+1))
	if err != nil {
		return nil, "", err
	}
	if len(b) > maxLogo {
		return nil, "", fmt.Errorf("logo %s is larger than 64 KiB", path)
	}
	if bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")) {
		return b, "image/png", nil
	}
	head := bytes.TrimSpace(b)
	if (bytes.HasPrefix(head, []byte("<svg")) || bytes.HasPrefix(head, []byte("<?xml"))) && bytes.Contains(b, []byte("<svg")) {
		return b, "image/svg+xml", nil
	}
	return nil, "", errors.New("logo must be a PNG or an SVG file")
}

// Traffic is the handler behind the load balancer when the app sleeps.
func (s *Server) Traffic() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /_standby/status", s.status)
	mux.HandleFunc("GET /_standby/app.js", static(s.js, "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET /_standby/style.css", static(s.css, "text/css; charset=utf-8"))
	mux.HandleFunc("GET /_standby/logo", s.serveLogo)
	mux.HandleFunc("POST /_standby/wake", s.wakeButton)
	mux.HandleFunc("/_standby/", http.NotFound)
	// Technical URLs never wake the stack.
	mux.HandleFunc("GET /robots.txt", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "User-agent: *\nDisallow: /\n")
	})
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("/.well-known/", http.NotFound)
	mux.HandleFunc("/", s.visit)
	return secure(mux)
}

// Health answers the load balancer: 200 while the guardian should receive
// visitors (the app is not fully up), 503 while the app serves.
func Health(ctrl Controller) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if ctrl.Healthy() {
			_, _ = io.WriteString(w, "standby serving\n")
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, "app serving\n")
	})
	return mux
}

func secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", pageCSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func static(body []byte, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(body)
	}
}

func (s *Server) serveLogo(w http.ResponseWriter, r *http.Request) {
	if s.logo == nil {
		http.NotFound(w, r)
		return
	}
	// An SVG opened directly must not run scripts on the app's origin.
	w.Header().Set("Content-Security-Policy", sandboxCSP)
	w.Header().Set("Content-Type", s.logoCT)
	_, _ = w.Write(s.logo)
}

func (s *Server) status(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.ctrl.Status())
}

func (s *Server) visit(w http.ResponseWriter, r *http.Request) {
	if s.wake.OnVisit {
		s.ctrl.RequestWake("visit")
	}
	st := s.ctrl.Status()
	retry := 30
	if st.ETASeconds > 0 {
		retry = min(max(st.ETASeconds, 5), 300)
	}
	w.Header().Set("Retry-After", strconv.Itoa(retry))
	if !strings.Contains(r.Header.Get("Accept"), "text/html") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": st.Phase, "retry_after": retry})
		return
	}
	data := pageData{
		Lang: s.page.Lang, Title: s.page.Title, Message: s.page.Message, Contact: s.page.Contact,
		HasLogo: s.logo != nil, ShowETA: s.page.ShowETA, Visit: s.wake.OnVisit, T: s.text,
		Setup: s.setup,
	}
	if data.Title == "" {
		data.Title = s.text.DefaultTitle
	}
	if data.Message == "" {
		data.Message = s.text.DefaultMessage
	}
	if s.wake.Button {
		data.Button = true
		data.Token = s.token()
	}
	var buf bytes.Buffer
	if err := s.tmpl.Execute(&buf, data); err != nil {
		http.Error(w, "standby page unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write(buf.Bytes())
}

func (s *Server) wakeButton(w http.ResponseWriter, r *http.Request) {
	if !s.wake.Button {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if err := r.ParseForm(); err != nil || !s.validToken(r.PostForm.Get("token")) {
		http.Error(w, "expired page, reload it", http.StatusForbidden)
		return
	}
	s.ctrl.RequestWake("button")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// token is a timestamp signed with a per-process key, valid tokenTTL.
func (s *Server) token() string {
	b := make([]byte, 8, 8+sha256.Size)
	binary.BigEndian.PutUint64(b, uint64(s.Now().Unix()))
	mac := hmac.New(sha256.New, s.key)
	mac.Write(b)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(b))
}

func (s *Server) validToken(tok string) bool {
	b, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil || len(b) != 8+sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, s.key)
	mac.Write(b[:8])
	if !hmac.Equal(mac.Sum(nil), b[8:]) {
		return false
	}
	issued := time.Unix(int64(binary.BigEndian.Uint64(b[:8])), 0)
	age := s.Now().Sub(issued)
	return age >= 0 && age <= tokenTTL
}

type pageData struct {
	Lang, Title, Message, Contact string
	HasLogo, ShowETA, Visit       bool
	Button                        bool
	Token                         string
	Setup                         []string
	T                             texts
}

// setupLines describes the schedule and idle rule in plain words.
func setupLines(cfg *config.Config, t texts) []string {
	lines := []string{fmt.Sprintf(t.IdleRule, humanDuration(cfg.IdleAfter.Duration))}
	if cfg.Wake.OnVisit {
		lines = append(lines, t.WakeVisit)
	}
	if cfg.Wake.Button {
		lines = append(lines, t.WakeButton)
	}
	if s := cfg.Schedule; s != nil {
		lines = append(lines, fmt.Sprintf(t.Ranges, strings.Join(s.Awake, ", "), strings.Join(s.Days, " "), s.Timezone))
	} else {
		lines = append(lines, t.NoRanges)
	}
	return lines
}

func humanDuration(d time.Duration) string {
	if d < time.Hour {
		return fmt.Sprintf("%d min", int(d.Minutes()))
	}
	if d%time.Hour == 0 {
		return fmt.Sprintf("%d h", int(d.Hours()))
	}
	return fmt.Sprintf("%d h %02d", int(d.Hours()), int(d.Minutes())%60)
}

// stylesheet only interpolates colours already validated as #rrggbb.
func stylesheet(p config.Page) []byte {
	return []byte(fmt.Sprintf(`:root{--primary:%s;--bg:%s;--text:%s}
*{box-sizing:border-box}
html,body{height:100%%}
body{margin:0;display:flex;align-items:center;justify-content:center;padding:16px;background:var(--bg);color:var(--text);font:16px/1.5 system-ui,-apple-system,"Segoe UI",Roboto,sans-serif}
main{width:100%%;max-width:480px;text-align:center}
.logo{max-width:96px;max-height:96px;margin:0 auto 16px;display:block}
h1{font-size:1.5rem;font-weight:600;margin:0 0 8px}
.message{margin:0 0 24px;opacity:.85}
progress{width:100%%;height:8px;accent-color:var(--primary)}
.status{margin:12px 0 0;font-weight:500}
.eta{margin:4px 0 0;opacity:.7;font-size:.9rem;min-height:1.4em}
button{margin-top:20px;background:var(--primary);color:#fff;border:0;border-radius:6px;padding:10px 20px;font:inherit;cursor:pointer}
button:focus-visible,a:focus-visible{outline:2px solid var(--primary);outline-offset:3px}
a{color:var(--primary)}
.contact{margin-top:24px;font-size:.9rem}
button:disabled{opacity:.45;cursor:default}
.setup{margin:28px 0 0;padding:14px 16px;border:1px solid rgba(127,127,127,.35);border-radius:8px;text-align:left;font-size:.9rem}
.setup h2{font-size:.95rem;margin:0 0 6px}
.setup ul{margin:0;padding-left:18px}
`, p.Primary, p.Background, p.Text))
}
