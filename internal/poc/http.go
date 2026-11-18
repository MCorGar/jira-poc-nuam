package poc

import (
	"bytes"
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

//go:embed web/*
var web embed.FS

type API struct {
	Store          Repository
	Provider       Provider
	Token          string
	Policy         Policy
	RequestTimeout time.Duration
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func detail(w http.ResponseWriter, status int, msg any) {
	writeJSON(w, status, map[string]any{"detail": msg})
}
func (a *API) protected(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.Token != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+a.Token)) != 1 {
			detail(w, 401, "Credencial de API inválida")
			return
		}
		timeout := a.RequestTimeout
		if timeout == 0 {
			timeout = 60 * time.Second
		}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		next(w, r.WithContext(ctx))
	}
}
func (a *API) catalog(w http.ResponseWriter, r *http.Request) (*Catalog, map[string]any) {
	c, report, err := a.Store.Catalog(r.Context())
	if err != nil {
		writeJSON(w, 503, map[string]any{"error": "base_de_datos_no_disponible", "reintentable": true})
		return nil, nil
	}
	if c == nil {
		detail(w, 503, "Catálogo pendiente de importar")
		return nil, nil
	}
	return c, report
}
func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"status": "ok", "modo": a.Provider.Mode(), "provider": a.Provider.Name(), "jira_modificado": false, "implementation": "go", "orm": "gorm"})
	})
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		c, report := a.catalog(w, r.WithContext(ctx))
		if c == nil {
			return
		}
		writeJSON(w, 200, map[string]any{"status": "ready", "catalogo_version": c.Version, "counts": report["counts"], "modo": a.Provider.Mode()})
	})
	mux.HandleFunc("GET /openapi.json", func(w http.ResponseWriter, r *http.Request) {
		data, _ := web.ReadFile("web/openapi.json")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
	})
	mux.HandleFunc("GET /docs", func(w http.ResponseWriter, r *http.Request) {
		data, _ := web.ReadFile("web/docs.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
	})
	mux.HandleFunc("GET /catalogo/resumen", a.protected(func(w http.ResponseWriter, r *http.Request) {
		c, report := a.catalog(w, r)
		if c != nil {
			writeJSON(w, 200, report)
		}
	}))
	mux.HandleFunc("GET /catalogo/{kind}", a.protected(a.list))
	mux.HandleFunc("POST /clasificaciones", a.protected(a.classify))
	return mux
}
func (a *API) list(w http.ResponseWriter, r *http.Request) {
	allowed := map[string]string{"servicios": "services", "solicitudes": "requests"}
	kind, ok := allowed[r.PathValue("kind")]
	if !ok {
		detail(w, 404, "Recurso de catálogo inexistente")
		return
	}
	q := r.URL.Query()
	limit, offset := 25, 0
	var err error
	if q.Has("limit") {
		limit, err = strconv.Atoi(q.Get("limit"))
		if err != nil || limit < 1 || limit > 100 {
			detail(w, 422, "limit debe estar entre 1 y 100")
			return
		}
	}
	if q.Has("offset") {
		offset, err = strconv.Atoi(q.Get("offset"))
		if err != nil || offset < 0 {
			detail(w, 422, "offset debe ser positivo")
			return
		}
	}
	if utf8.RuneCountInString(q.Get("q")) > 200 {
		detail(w, 422, "q demasiado largo")
		return
	}
	c, _ := a.catalog(w, r)
	if c == nil {
		return
	}
	items := []Entity{}
	for _, item := range c.Groups()[kind] {
		if q.Get("q") != "" && !strings.Contains(Normalize(str(item, "name")), Normalize(q.Get("q"))) {
			continue
		}
		if q.Get("parent_code") != "" && str(item, "parent_code") != q.Get("parent_code") {
			continue
		}
		items = append(items, item)
	}
	start := min(offset, len(items))
	end := start + min(limit, len(items)-start)
	writeJSON(w, 200, map[string]any{"total": len(items), "items": items[start:end], "catalogo_version": c.Version})
}

func DecodeInput(data []byte) (ClassificationInput, error) {
	var t ClassificationInput
	if !json.Valid(data) || !utf8.Valid(data) {
		return t, errors.New("JSON inválido")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return t, errors.New("se requiere un objeto JSON")
	}
	for _, name := range []string{"titulo", "pais", "tipo_usuario", "descripcion"} {
		if v, ok := fields[name]; ok && bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return t, errors.New("los campos no admiten null")
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&t); err != nil {
		return t, errors.New("estructura o tipos de campos inválidos")
	}
	return t, t.Validate()
}
func (a *API) classify(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 128<<10))
	if err != nil {
		detail(w, 413, "Cuerpo demasiado grande")
		return
	}
	t, err := DecodeInput(data)
	if err != nil {
		detail(w, 422, []map[string]any{{"type": "value_error", "msg": err.Error(), "loc": []string{"body"}}})
		return
	}
	c, _ := a.catalog(w, r)
	if c == nil {
		return
	}
	policy := a.Policy.normalized()
	key := IdempotencyKey(t, c.Version, a.Provider.CacheKey(), policy)
	cached, err := a.Store.FindDecision(r.Context(), key)
	if err != nil {
		writeJSON(w, 503, map[string]any{"error": "base_de_datos_no_disponible", "reintentable": true})
		return
	}
	if cached != nil {
		cached["reutilizada"] = true
		writeJSON(w, 200, cached)
		return
	}
	result, err := Classify(r.Context(), c, a.Provider, t, policy)
	if errors.Is(err, ErrDecisionBudget) {
		detail(w, 413, map[string]any{"error": "entrada_o_opcion_supera_presupuesto_modelo", "reintentable": false})
		return
	}
	if errors.Is(err, ErrModelContext) {
		detail(w, 503, map[string]any{"error": "proveedor_limite_contexto", "reintentable": false})
		return
	}
	if errors.Is(err, ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
		detail(w, 504, map[string]any{"error": "proveedor_timeout", "reintentable": true})
		return
	}
	if err != nil {
		detail(w, 503, map[string]any{"error": "proveedor_no_disponible", "reintentable": true})
		return
	}
	saved, err := a.Store.SaveDecision(r.Context(), key, result)
	if err != nil {
		writeJSON(w, 503, map[string]any{"error": "base_de_datos_no_disponible", "reintentable": true})
		return
	}
	writeJSON(w, 200, saved)
}
