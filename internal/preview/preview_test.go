package preview

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/simondelamarre/standby-bige-ops/internal/controller"
)

func TestPreviewSimulatesAWakeAndStartsOver(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	c := New(3, 9*time.Second)
	c.Now = func() time.Time { return now }
	page := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "waiting page") })
	h := Handler(c, page)
	get := func() string {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		return rec.Body.String()
	}

	if c.Status().Phase != controller.Asleep || !c.RequestWake("visit") || c.RequestWake("visit") {
		t.Fatal("the first wake must start once")
	}
	now = now.Add(4 * time.Second)
	if st := c.Status(); st.Phase != controller.Waking || st.Step != 1 || st.ETASeconds != 6 {
		t.Fatalf("mid-wake status = %+v", st)
	}
	if get() != "waiting page" {
		t.Fatal("the waiting page must show while waking")
	}
	now = now.Add(6 * time.Second)
	if !strings.Contains(get(), "Ici, ton app répondrait") {
		t.Fatal("the app placeholder must show once awake")
	}
	if c.Status().Phase != controller.Asleep {
		t.Fatal("the preview must start over")
	}
}
