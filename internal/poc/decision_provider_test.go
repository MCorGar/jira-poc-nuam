package poc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func providerOptions() []Entity {
	return []Entity{{"code": "SOL_A", "name": "Crear cuenta", "description": "Alta", "requirements": "Carta firmada", "countries": []string{"Chile"}, "service_name": "Accesos"}}
}

const providerState = `{"titulo":"Crear cuenta","pais":"Chile","tipo_usuario":"Colaborador","descripcion":"Alta"}`

func TestDecisionProviderRequestAndResponse(t *testing.T) {
	for _, list := range []bool{false, true} {
		t.Run(fmt.Sprint(list), func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/providers/microsoft/v1/systemone" || r.Header.Get("api-key") != "test-secret" || r.Header.Get("Content-Type") != "application/json" {
					t.Error("contrato HTTP incorrecto")
				}
				var payload struct {
					Model     string              `json:"model"`
					State     ClassificationInput `json:"state"`
					Questions map[string]struct {
						Type         string            `json:"type"`
						Instructions string            `json:"instructions"`
						Criteria     map[string]string `json:"criteria"`
					} `json:"questions"`
				}
				if json.NewDecoder(r.Body).Decode(&payload) != nil || payload.Model != "deployment-test" || payload.State.Country != "Chile" || payload.State.UserType != "Colaborador" || payload.State.Title != "Crear cuenta" {
					t.Errorf("entrada: %+v", payload)
				}
				question := payload.Questions["solicitud"]
				if len(payload.Questions) != 1 || question.Type != "choice" || len(question.Criteria) != 2 || !strings.Contains(question.Criteria["SOL_A"], "Accesos") || !strings.Contains(question.Criteria["SOL_A"], "Carta firmada") || !strings.Contains(question.Criteria["SOL_A"], "País:") || question.Criteria[abstainCode] == "" || question.Instructions == "" {
					t.Error("pregunta incorrecta")
				}
				probs := `{"SOL_A":0.94,"__SIN_COINCIDENCIA__":0.06}`
				if list {
					probs = `[{"value":"SOL_A","probability":0.94},{"value":"__SIN_COINCIDENCIA__","probability":0.06}]`
				}
				fmt.Fprintf(w, `{"model":"microsoft-decision-1","answers":{"solicitud":{"type":"choice","choice":"SOL_A","confidence":0.2,"probabilities":%s}}}`, probs)
			}))
			defer server.Close()
			p := &DecisionProvider{endpoint: server.URL + "/providers/microsoft/v1/systemone", deployment: "deployment-test", apiKey: "test-secret", client: server.Client()}
			ranking, err := p.rankOnce(context.Background(), providerState, providerOptions(), "solicitud")
			if err != nil || len(ranking.Candidates) != 2 || ranking.Candidates[0] != (Candidate{"SOL_A", .94}) || ranking.Model != "microsoft-decision-1" {
				t.Fatalf("%+v %v", ranking, err)
			}
			input, _ := DecodeInput([]byte(providerState))
			catalog := syntheticCatalog()
			catalog.Requests = catalog.Requests[:1]
			r, err := Classify(context.Background(), catalog, p, input, Policy{})
			if err != nil || r["modo"] != "real" || r["probabilidad_solicitud"] != .94 {
				t.Fatalf("%v %v", r, err)
			}
			if strings.Contains(p.CacheKey(), "test-secret") {
				t.Fatal("secreto en clave")
			}
		})
	}
}
func TestDecisionProviderRejectsMalformedResponses(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"error", "secret provider error", 500},
		{"json", "not-json", 200},
		{"sin_respuestas", `{"model":"m"}`, 200},
		{"sin_probabilidades", `{"model":"m","answers":{"solicitud":{"type":"choice","choice":"SOL_A"}}}`, 200},
		{"desconocida", `{"model":"m","answers":{"solicitud":{"type":"choice","choice":"BAD","probabilities":{"SOL_A":0.9,"__SIN_COINCIDENCIA__":0.1}}}}`, 200},
		{"seleccion_inconsistente", `{"model":"m","answers":{"solicitud":{"type":"choice","choice":"SOL_A","probabilities":{"SOL_A":0.1,"__SIN_COINCIDENCIA__":0.9}}}}`, 200},
		{"incompleta", `{"model":"m","answers":{"solicitud":{"type":"choice","choice":"SOL_A","probabilities":{"SOL_A":0.99}}}}`, 200},
		{"no_normalizada", `{"model":"m","answers":{"solicitud":{"type":"choice","choice":"SOL_A","probabilities":{"SOL_A":0.9,"__SIN_COINCIDENCIA__":0.9}}}}`, 200},
		{"null", `{"model":"m","answers":{"solicitud":{"type":"choice","choice":"SOL_A","probabilities":{"SOL_A":1,"__SIN_COINCIDENCIA__":null}}}}`, 200},
		{"negativa", `{"model":"m","answers":{"solicitud":{"type":"choice","choice":"SOL_A","probabilities":{"SOL_A":1.1,"__SIN_COINCIDENCIA__":-0.1}}}}`, 200},
		{"duplicada", `{"model":"m","answers":{"solicitud":{"type":"choice","choice":"SOL_A","probabilities":[{"value":"SOL_A","probability":0.9},{"value":"SOL_A","probability":0.1}]}}}`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) }))
			defer server.Close()
			p := &DecisionProvider{endpoint: server.URL, client: server.Client()}
			_, err := p.rankOnce(context.Background(), providerState, providerOptions(), "solicitud")
			if !errors.Is(err, ErrProvider) || strings.Contains(err.Error(), "secret") {
				t.Fatal(err)
			}
		})
	}
}
func TestDecisionProviderTimeoutAndBearer(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" || r.Header.Get("api-key") != "" {
			t.Error("auth")
		}
		select {
		case <-r.Context().Done():
		case <-time.After(150 * time.Millisecond):
		}
	}))
	defer server.Close()
	client := server.Client()
	client.Timeout = 30 * time.Millisecond
	p := &DecisionProvider{endpoint: server.URL, token: "token", client: client}
	if _, err := p.rankOnce(context.Background(), providerState, providerOptions(), "solicitud"); !errors.Is(err, ErrTimeout) {
		t.Fatal(err)
	}
}
func TestProviderConfigurationAndPolicy(t *testing.T) {
	for _, key := range []string{"AZURE_ENDPOINT", "DEPLOYMENT_NAME", "AZURE_API_KEY", "AZURE_ENTRA_TOKEN", "DECISION_THRESHOLD", "DECISION_MIN_MARGIN"} {
		t.Setenv(key, "")
	}
	if _, err := NewProvider("decision-1", "none"); err == nil {
		t.Fatal("credenciales ausentes")
	}
	if _, err := NewProvider("unknown", "none"); err == nil {
		t.Fatal("fallback")
	}
	if _, err := NewProvider("mock", "invalid"); err == nil {
		t.Fatal("failure inválido")
	}
	t.Setenv("AZURE_ENDPOINT", "https://resource.cognitiveservices.azure.com")
	t.Setenv("DEPLOYMENT_NAME", "deployment")
	t.Setenv("AZURE_API_KEY", "secret")
	p, err := NewProvider("decision-1", "none")
	if err != nil || p.Mode() != "real" {
		t.Fatal(err)
	}
	original := p.CacheKey()
	t.Setenv("DEPLOYMENT_NAME", "different")
	p, err = NewProvider("decision-1", "none")
	if err != nil || p.CacheKey() == original {
		t.Fatal("despliegue reutilizado", err)
	}
	for _, endpoint := range []string{"http://example.com", "https://user:password@example.com", "https://example.com/path", "https://example.com?x=y"} {
		t.Setenv("AZURE_ENDPOINT", endpoint)
		if _, err := NewProvider("decision-1", "none"); err == nil {
			t.Fatal(endpoint)
		}
	}
	t.Setenv("AZURE_ENDPOINT", "https://example.com")
	t.Setenv("AZURE_ENTRA_TOKEN", "token")
	if _, err := NewProvider("decision-1", "none"); err == nil {
		t.Fatal("auth ambigua")
	}
	for _, value := range []string{"NaN", "Inf", "0", "1.1", "invalid"} {
		t.Setenv("DECISION_THRESHOLD", value)
		if _, err := PolicyFromEnv(); err == nil {
			t.Fatal("umbral", value)
		}
	}
	t.Setenv("DECISION_THRESHOLD", "0.92")
	t.Setenv("DECISION_MIN_MARGIN", "0.1")
	if p, err := PolicyFromEnv(); err != nil || p != (Policy{.92, .1}) {
		t.Fatal(p, err)
	}
}

func TestDecisionProviderBodyTimeout(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(150 * time.Millisecond):
		}
	}))
	defer server.Close()
	client := server.Client()
	client.Timeout = 30 * time.Millisecond
	p := &DecisionProvider{endpoint: server.URL, client: client}
	if _, err := p.rankOnce(context.Background(), providerState, providerOptions(), "solicitud"); !errors.Is(err, ErrTimeout) {
		t.Fatal(err)
	}
}

func TestRealProviderModeInHealthAndReadiness(t *testing.T) {
	h := (&API{Store: syntheticStore(), Provider: &fixedProvider{}}).Handler()
	for _, path := range []string{"/health", "/ready"} {
		rr := call(h, "GET", path, "", "")
		var body map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &body)
		if rr.Code != 200 || body["modo"] != "real" {
			t.Fatal(rr.Code, body)
		}
	}
}
