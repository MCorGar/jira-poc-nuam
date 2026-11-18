package poc

import (
	"encoding/json"
	"os"
	"testing"
)

// Explicit opt-in: exercises the MS HTTP handler against real Azure inference,
// using only an in-memory synthetic catalog. No workbook or internal catalog is
// loaded or sent. Default unit-test runs never use credentials or call Azure.
func TestDecisionLiveSynthetic(t *testing.T) {
	if os.Getenv("DECISION_LIVE_TEST") != "1" {
		t.Skip("set DECISION_LIVE_TEST=1 and configure Foundry credentials to run the synthetic live test")
	}
	provider, err := NewProvider("decision-1", "none")
	if err != nil {
		t.Fatal(err)
	}
	store := syntheticStore()
	handler := (&API{Store: store, Provider: provider, Token: "synthetic-test-token"}).Handler()
	body := `{"titulo":"Crear cuenta","pais":"Chile","tipo_usuario":"Colaborador","descripcion":"Se incorpora una persona al equipo y necesita una cuenta nueva de acceso."}`
	first := call(handler, "POST", "/clasificaciones", body, "synthetic-test-token")
	var result Result
	if json.Unmarshal(first.Body.Bytes(), &result) != nil || first.Code != 200 {
		t.Fatalf("classification HTTP %d: %s", first.Code, first.Body.String())
	}
	request, _ := result["solicitud"].(map[string]any)
	service, _ := result["servicio"].(map[string]any)
	if result["modo"] != "real" || result["estado"] != "clasificado" || request["codigo"] != "SOL_A" || service["codigo"] != "SER_A" {
		t.Fatalf("unexpected synthetic result: %s", first.Body.String())
	}
	t.Logf("synthetic classification: request=%s service=%s probability=%v model=%v latency_ms=%v", request["nombre"], service["nombre"], result["probabilidad_solicitud"], result["modelo"], result["latencia_ms"])
	repeat := call(handler, "POST", "/clasificaciones", body, "synthetic-test-token")
	var reused Result
	_ = json.Unmarshal(repeat.Body.Bytes(), &reused)
	if repeat.Code != 200 || reused["decision_id"] != result["decision_id"] || reused["reutilizada"] != true {
		t.Fatal("synthetic idempotency check failed")
	}
	t.Log("synthetic idempotency: same decision_id, reutilizada=true")
	unrelated := `{"titulo":"Reservar restaurante","pais":"Chile","tipo_usuario":"Colaborador","descripcion":"Necesito una mesa para cenar con mi familia el sábado."}`
	abstention := call(handler, "POST", "/clasificaciones", unrelated, "synthetic-test-token")
	var clarification Result
	_ = json.Unmarshal(abstention.Body.Bytes(), &clarification)
	if abstention.Code != 200 || clarification["estado"] != "requiere_aclaracion" || clarification["solicitud"] != nil || clarification["servicio"] != nil {
		t.Fatalf("unexpected synthetic abstention: %s", abstention.Body.String())
	}
	t.Logf("synthetic out-of-catalog request: estado=%v motivo=%v", clarification["estado"], clarification["motivo"])
	invalid := call(handler, "POST", "/clasificaciones", `{"titulo":"Crear cuenta"}`, "synthetic-test-token")
	if invalid.Code != 422 {
		t.Fatalf("invalid input: HTTP %d", invalid.Code)
	}
	t.Log("missing fields: HTTP 422")
}

// Requires the separate opt-in for the internal catalog. The user explicitly
// authorized sending requests applicable to Chile to their Azure resource.
func TestDecisionLiveCatalog(t *testing.T) {
	if os.Getenv("DECISION_LIVE_CATALOG_TEST") != "1" {
		t.Skip("set DECISION_LIVE_CATALOG_TEST=1 only with authorization to send the internal Chile catalog to Azure")
	}
	provider, err := NewProvider("decision-1", "none")
	if err != nil {
		t.Fatal(err)
	}
	store := memory(t)
	handler := (&API{Store: store, Provider: provider, Token: "live-test-token"}).Handler()
	cases := []struct{ name, body, expected string }{
		{"crear-cuenta", `{"titulo":"Crear Cuenta","pais":"Chile","tipo_usuario":"Colaborador","descripcion":"Necesito crear usuarios con roles y perfiles de las aplicaciones core y no core para una persona nueva en ambiente productivo."}`, "Crear Cuenta"},
		{"backup", `{"titulo":"Backup Base de Datos","pais":"Chile","tipo_usuario":"Colaborador","descripcion":"Solicito realizar un backup de una base de datos para un laboratorio."}`, "Backup Base de Datos"},
		{"fuera-catalogo", `{"titulo":"Reservar restaurante","pais":"Chile","tipo_usuario":"Colaborador","descripcion":"Necesito una mesa para cenar con mi familia el sábado."}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := call(handler, "POST", "/clasificaciones", tc.body, "live-test-token")
			var body Result
			_ = json.Unmarshal(rr.Body.Bytes(), &body)
			if rr.Code != 200 {
				t.Fatalf("HTTP %d: %s", rr.Code, rr.Body.String())
			}
			if body["modo"] != "real" {
				t.Fatal("real provider was not used")
			}
			if tc.expected == "" {
				if body["estado"] != "requiere_aclaracion" || body["solicitud"] != nil || body["servicio"] != nil {
					t.Fatalf("expected abstention: %s", rr.Body.String())
				}
			} else {
				request, _ := body["solicitud"].(map[string]any)
				service, _ := body["servicio"].(map[string]any)
				if body["estado"] != "clasificado" || request["nombre"] != tc.expected {
					t.Fatalf("unexpected classification: %s", rr.Body.String())
				}
				found := false
				for _, r := range store.c.Requests {
					if str(r, "code") == request["codigo"] {
						found = str(r, "parent_code") == service["codigo"]
					}
				}
				if !found {
					t.Fatal("service does not belong to classified request")
				}
			}
			stages, _ := body["etapas"].([]any)
			for _, stage := range stages {
				if s, ok := stage.(map[string]any); ok && s["bytes_peticion"].(float64) > 24000 {
					t.Fatal("request budget exceeded", s)
				}
			}
			t.Logf("strategy=%v state=%v request=%v service=%v probability=%v calls=%d latency_ms=%v", body["estrategia"], body["estado"], body["solicitud"], body["servicio"], body["probabilidad_solicitud"], len(stages), body["latencia_ms"])
			repeated := call(handler, "POST", "/clasificaciones", tc.body, "live-test-token")
			var again Result
			_ = json.Unmarshal(repeated.Body.Bytes(), &again)
			if repeated.Code != 200 || again["decision_id"] != body["decision_id"] || again["reutilizada"] != true {
				t.Fatal("idempotency failed")
			}
		})
	}
}
