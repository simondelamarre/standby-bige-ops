// Package preview serves the waiting page with a simulated wake-up, so a page
// can be tuned (name, logo, colours, texts) without any machine.
package preview

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/simondelamarre/standby-bige-ops/internal/controller"
)

// Controller pretends to wake a stack in Steps steps over Wake.
type Controller struct {
	Steps int
	Wake  time.Duration
	Now   func() time.Time

	mu      sync.Mutex
	waking  bool
	started time.Time
}

func New(steps int, wake time.Duration) *Controller {
	return &Controller{Steps: steps, Wake: wake, Now: time.Now}
}

func (c *Controller) Status() controller.Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := controller.Status{Phase: controller.Asleep, Steps: c.Steps, CanWake: true}
	if !c.waking {
		return st
	}
	elapsed := c.Now().Sub(c.started)
	if elapsed >= c.Wake {
		st.Phase, st.Step = controller.Awake, c.Steps
		return st
	}
	st.Phase = controller.Waking
	st.Step = int(elapsed * time.Duration(c.Steps) / c.Wake)
	st.ETASeconds = int((c.Wake - elapsed).Seconds()) + 1
	return st
}

func (c *Controller) RequestWake(string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.waking {
		return false
	}
	c.waking, c.started = true, c.Now()
	return true
}

func (c *Controller) Healthy() bool { return true }

func (c *Controller) reset() {
	c.mu.Lock()
	c.waking = false
	c.mu.Unlock()
}

const appPage = `<!doctype html><html lang="fr"><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Aperçu terminé</title>
<body style="font:17px/1.6 system-ui,sans-serif;max-width:520px;margin:15vh auto;padding:0 16px">
<h1>Ici, ton app répondrait.</h1>
<p>L'aperçu du réveil est terminé. <a href="/">Recommencer</a></p>
</body></html>
`

// Handler serves the waiting page, then a placeholder for the app once the
// simulated wake-up ends, and starts over.
func Handler(c *Controller, page http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/_standby/") && c.Status().Phase == controller.Awake {
			c.reset()
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, appPage)
			return
		}
		page.ServeHTTP(w, r)
	})
}
