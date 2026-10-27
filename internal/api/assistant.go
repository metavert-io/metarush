package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/mux"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/assistant"
)

// Dashboard assistant endpoints (local Claude Code conversations) outside the
// op registry: availability, the live stream of a conversation (SSE) and the
// images Claude looked at. Everything else is an op (POST /api/ops/assistant.*).
// All sit behind requireSession: a signed-in session cookie, plus the
// X-MetaRush-Client header on POSTs (CSRF). Conversations never change shared
// state; the dashboard applies their proposals and runs their action cards
// through the ordinary ops endpoint (the human channel).
func (s *Server) assistantRoutes(r *mux.Router) {
	r.HandleFunc("/assistant", s.assistantStatus).Methods("GET")
	r.HandleFunc("/assistant/conversations/{id}/events", s.assistantEvents).Methods("GET")
	r.HandleFunc("/assistant/image", s.assistantImage).Methods("GET", "HEAD")
	r.HandleFunc("/assistant/conversations/{id}/attachments/{sha}", s.assistantAttachment).Methods("GET", "HEAD")
}

// assistantAttachment serves a picture or PDF the person attached to a
// conversation's message (D166): only files its messages name, from the media
// store by content, never anything else.
func (s *Server) assistantAttachment(w http.ResponseWriter, r *http.Request) {
	v := mux.Vars(r)
	f, info, err := s.Assistant.OpenAttachment(r.Context(), v["id"], v["sha"])
	if err != nil {
		ae := apperr.From(err)
		status := http.StatusBadRequest
		if ae.Code == apperr.NotFound {
			status = http.StatusNotFound
		}
		http.Error(w, ae.Message, status)
		return
	}
	defer f.Close()
	h := w.Header()
	h.Set("Content-Type", info.MIME)
	h.Set("Content-Length", strconv.FormatInt(info.Size, 10))
	h.Set("Cache-Control", "private, max-age=86400, immutable")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	h.Set("Content-Disposition", "inline")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.Copy(w, f)
}

func (s *Server) assistantStatus(w http.ResponseWriter, r *http.Request) {
	// The count of cards waiting is the person's own (remote access, D129).
	ctx := assistant.AsPerson(r.Context(), s.appFor(r, "dashboard"))
	writeOK(w, s.Assistant.Status(ctx, r.URL.Query().Get("refresh") == "1"))
}

// assistantEvents streams a conversation as Server-Sent Events: every transcript
// event after Last-Event-ID (or ?after=) as an unnamed message with id = seq;
// `meta` with the conversation (turns, queue, actions, cost) whenever it
// changes; and `partial` with text Claude is still writing (from = 0 replaces,
// otherwise append). The stream stays open while the page is; conversations
// held by another process or machine are polled.
func (s *Server) assistantEvents(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	after := 0
	for _, v := range []string{r.Header.Get("Last-Event-ID"), r.URL.Query().Get("after")} {
		if n, err := strconv.Atoi(v); err == nil && n > after {
			after = n
		}
	}
	raw := r.URL.Query().Get("raw") == "1"
	// The conversation as the request's person sees it: whose it is and what
	// waits for them (D179).
	ctx := assistant.AsPerson(r.Context(), s.appFor(r, "dashboard"))
	st, err := s.Assistant.Follow(ctx, id, after, -1, -1, raw)
	if err != nil {
		writeErr(w, err)
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	fmt.Fprint(w, "retry: 2000\n\n")
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	metaVer, partialVer := -1, -1
	block, sent := -1, 0
	for {
		for _, e := range st.Events {
			b, _ := json.Marshal(e)
			fmt.Fprintf(w, "id: %d\ndata: %s\n\n", e.Seq, b)
			after = e.Seq
		}
		if st.Meta != nil {
			b, _ := json.Marshal(st.Meta)
			fmt.Fprintf(w, "event: meta\ndata: %s\n\n", b)
		}
		metaVer = st.MetaVer
		if p := st.Partial; p != nil {
			out := assistant.AssistantPartial{Turn: p.Turn, Block: p.Block, Phase: p.Phase, Text: p.Text}
			if p.Block == block && len(p.Text) >= sent && sent > 0 {
				out.From, out.Text = sent, p.Text[sent:]
			}
			if out.From == 0 || out.Text != "" {
				b, _ := json.Marshal(out)
				fmt.Fprintf(w, "event: partial\ndata: %s\n\n", b)
			}
			block, sent = p.Block, len(p.Text)
		}
		partialVer = st.PartialVer
		fl.Flush()
		select {
		case <-r.Context().Done():
			return
		case <-st.Wait:
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		}
		if st, err = s.Assistant.Follow(ctx, id, after, metaVer, partialVer, raw); err != nil {
			return
		}
	}
}

// assistantImage serves an image Claude Code looked at (a frame or contact
// sheet) for the thread's thumbnails. Only images inside the scratch folder or
// the assistant folder are served; see assistant.Manager.OpenImage.
func (s *Server) assistantImage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f, ct, size, err := s.Assistant.OpenImage(q.Get("root"), q.Get("path"))
	if err != nil {
		ae := apperr.From(err)
		status := http.StatusBadRequest
		if ae.Code == apperr.NotFound {
			status = http.StatusNotFound
		}
		http.Error(w, ae.Message, status)
		return
	}
	defer f.Close()
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("Content-Length", strconv.FormatInt(size, 10))
	h.Set("Cache-Control", "private, max-age=300")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.Copy(w, f)
}
