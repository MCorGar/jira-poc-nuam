package poc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
)

type decisionCall struct {
	phase    string
	criteria map[string]string
	bytes    int
}

func hierarchyServer(t *testing.T, targetProbability float64, abstainRouting bool) (*DecisionProvider, *[]decisionCall, func()) {
	t.Helper()
	calls := []decisionCall{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var input struct {
			Questions map[string]struct {
				Criteria map[string]string `json:"criteria"`
			} `json:"questions"`
		}
		if json.Unmarshal(raw, &input) != nil {
			t.Error("invalid request")
		}
		for phase, question := range input.Questions {
			calls = append(calls, decisionCall{phase, question.Criteria, len(raw)})
			keys := []string{}
			for key := range question.Criteria {
				if key != abstainCode {
					keys = append(keys, key)
				}
			}
			sort.Strings(keys)
			choice := keys[0]
			probability := .99
			if phase == "servicio" && abstainRouting {
				choice = abstainCode
			} else if _, ok := question.Criteria["TARGET"]; ok {
				choice = "TARGET"
				probability = targetProbability
			}
			probabilities := map[string]float64{}
			for key := range question.Criteria {
				probabilities[key] = (1 - probability) / float64(len(question.Criteria)-1)
			}
			probabilities[choice] = probability
			_ = json.NewEncoder(w).Encode(map[string]any{"model": "test-model", "answers": map[string]any{phase: map[string]any{"type": "choice", "choice": choice, "probabilities": probabilities}}})
		}
	}))
	p := &DecisionProvider{endpoint: server.URL, deployment: "test", client: server.Client(), maxBytes: 5000, serviceCandidates: 2}
	return p, &calls, server.Close
}

func largeOptions() []Entity {
	items := []Entity{}
	for i := 0; i < 40; i++ {
		code := fmt.Sprintf("REQ_%02d", i)
		if i == 39 {
			code = "TARGET"
		}
		items = append(items, Entity{"code": code, "name": "Solicitud " + code, "description": strings.Repeat("descripción completa ", 10), "requirements": strings.Repeat("requisito íntegro ", 12), "service_code": "S1", "service_name": "Servicio uno"})
	}
	return items
}
func TestBatchingKeepsEveryOptionAndUsesOnlyFinalProbabilities(t *testing.T) {
	p, calls, closeServer := hierarchyServer(t, .55, false)
	defer closeServer()
	options := largeOptions()
	before, _ := json.Marshal(options)
	result, err := p.boundedRank(context.Background(), providerState, options, "solicitud")
	if err != nil {
		t.Fatal(err)
	}
	if result.Candidates[0] != (Candidate{"TARGET", .55}) {
		t.Fatalf("batch probabilities compared globally: %+v", result)
	}
	if len(*calls) < 3 || len(result.Stages) != len(*calls) {
		t.Fatal("missing stages", result.Stages)
	}
	seen := map[string]bool{}
	for _, call := range *calls {
		if call.bytes > p.maxBytes {
			t.Fatal("oversized request", call.bytes)
		}
		for code, text := range call.criteria {
			if code != abstainCode {
				seen[code] = true
				if !strings.Contains(text, strings.Repeat("requisito íntegro ", 12)) {
					t.Fatal("truncated requirements")
				}
			}
		}
	}
	if len(seen) != len(options) {
		t.Fatalf("lost options: %d of %d", len(seen), len(options))
	}
	final := (*calls)[len(*calls)-1]
	if _, ok := final.criteria["TARGET"]; !ok {
		t.Fatal("target not compared in final call")
	}
	if len(final.criteria) < 3 {
		t.Fatal("missing comparison of batch finalists")
	}
	after, _ := json.Marshal(options)
	if string(before) != string(after) {
		t.Fatal("catalog mutated")
	}
}
func TestHierarchyUsesTwoServicesAndDerivesFinalService(t *testing.T) {
	p, calls, closeServer := hierarchyServer(t, .96, false)
	defer closeServer()
	c := &Catalog{Schema: CatalogSchema, Version: "synthetic-hierarchy", Services: []Entity{}, Requests: []Entity{}}
	for service := 1; service <= 3; service++ {
		sid := fmt.Sprintf("S%d", service)
		c.Services = append(c.Services, Entity{"code": sid, "name": "Servicio " + sid})
		for i := 0; i < 15; i++ {
			code := fmt.Sprintf("REQ_%s_%02d", sid, i)
			if service == 2 && i == 14 {
				code = "TARGET"
			}
			c.Requests = append(c.Requests, Entity{"code": code, "parent_code": sid, "name": "Solicitud " + code, "description": strings.Repeat("detalle completo ", 15), "requirements": strings.Repeat("requisito completo ", 12), "countries": []string{"Chile"}})
		}
	}
	original, _ := json.Marshal(c)
	input, _ := DecodeInput([]byte(validInput))
	result, err := Classify(context.Background(), c, p, input, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if result["estado"] != "clasificado" || result["estrategia"] != "jerarquica" || str(result["solicitud"].(Entity), "codigo") != "TARGET" || str(result["servicio"].(Entity), "codigo") != "S2" {
		t.Fatal(result)
	}
	if (*calls)[0].phase != "servicio" {
		t.Fatal("missing service routing")
	}
	for _, request := range c.Requests {
		if !strings.Contains((*calls)[0].criteria[str(request, "parent_code")], str(request, "name")) {
			t.Fatal("routing omitted request name")
		}
	}
	sawS2 := false
	for _, call := range (*calls)[1:] {
		if call.phase != "solicitud" || call.bytes > 5000 {
			t.Fatal(call.phase, call.bytes)
		}
		for code := range call.criteria {
			if strings.HasPrefix(code, "REQ_S3") {
				t.Fatal("unselected service sent")
			}
			if strings.HasPrefix(code, "REQ_S2") || code == "TARGET" {
				sawS2 = true
			}
		}
	}
	if !sawS2 {
		t.Fatal("second candidate service not evaluated")
	}
	after, _ := json.Marshal(c)
	if string(original) != string(after) {
		t.Fatal("catalog changed")
	}
}
func TestServiceAbstentionDoesNotInventRequestProbability(t *testing.T) {
	p, calls, closeServer := hierarchyServer(t, .96, true)
	defer closeServer()
	options := largeOptions()
	for i, option := range options {
		if i%2 == 0 {
			option["service_code"] = "S2"
			option["service_name"] = "Servicio dos"
		}
	}
	result, err := p.Rank(context.Background(), providerState, options)
	if err != nil || result.Candidates[0].Code != abstainCode || len(*calls) != 1 || result.Stages[0]["fase"] != "servicio" {
		t.Fatalf("%+v %v", result, err)
	}
}
func TestBudgetRejectsSingleHugeOptionWithoutSending(t *testing.T) {
	p, calls, closeServer := hierarchyServer(t, .96, false)
	defer closeServer()
	options := providerOptions()
	options[0]["requirements"] = strings.Repeat("x", 10000)
	_, err := p.Rank(context.Background(), providerState, options)
	if !errors.Is(err, ErrDecisionBudget) || len(*calls) != 0 {
		t.Fatal(err, len(*calls))
	}
}
func TestBatchingStopsWhenFinalistsCannotFitTogether(t *testing.T) {
	p, calls, closeServer := hierarchyServer(t, .96, false)
	defer closeServer()
	options := []Entity{{"code": "A", "name": "A", "requirements": strings.Repeat("x", 3000)}, {"code": "B", "name": "B", "requirements": strings.Repeat("y", 3000)}}
	_, err := p.boundedRank(context.Background(), providerState, options, "solicitud")
	if !errors.Is(err, ErrDecisionBudget) || len(*calls) != 2 {
		t.Fatal(err, len(*calls))
	}
}
func TestHierarchyTechnicalErrorsAreNotPersisted(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{ErrDecisionBudget, 413, "entrada_o_opcion_supera_presupuesto_modelo"},
		{ErrModelContext, 503, "proveedor_limite_contexto"},
	} {
		store := syntheticStore()
		handler := (&API{Store: store, Provider: &fixedProvider{err: tc.err}}).Handler()
		response := call(handler, "POST", "/clasificaciones", validInput, "")
		var body struct {
			Detail struct {
				Error string `json:"error"`
				Retry bool   `json:"reintentable"`
			} `json:"detail"`
		}
		_ = json.Unmarshal(response.Body.Bytes(), &body)
		if response.Code != tc.status || body.Detail.Error != tc.code || body.Detail.Retry || len(store.results) != 0 {
			t.Fatal(response.Code, response.Body.String(), len(store.results))
		}
	}
}
func TestKnownAzureContextRejection(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(422)
		fmt.Fprint(w, `{"detail":"invalid model request: Question prompt exceeds the 32768-token context limit including the decision token"}`)
	}))
	defer server.Close()
	p := &DecisionProvider{endpoint: server.URL, client: server.Client()}
	if _, err := p.Rank(context.Background(), providerState, providerOptions()); !errors.Is(err, ErrModelContext) {
		t.Fatal(err)
	}
}
func TestHierarchyConfigurationAndCacheIdentity(t *testing.T) {
	p := &DecisionProvider{endpoint: "https://example.test", deployment: "model"}
	first := p.CacheKey()
	p.maxBytes = 20000
	if p.CacheKey() == first {
		t.Fatal("budget absent from cache key")
	}
	second := p.CacheKey()
	p.serviceCandidates = 3
	if p.CacheKey() == second {
		t.Fatal("routing absent from cache key")
	}
	for name, value := range map[string]string{"DECISION_MAX_REQUEST_BYTES": "30000", "DECISION_SERVICE_CANDIDATES": "0", "DECISION_TIMEOUT_SECONDS": "NaN", "CLASSIFICATION_TIMEOUT_SECONDS": "4"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("AZURE_ENDPOINT", "https://example.test")
			t.Setenv("DEPLOYMENT_NAME", "model")
			t.Setenv("AZURE_API_KEY", "synthetic-key")
			t.Setenv("AZURE_ENTRA_TOKEN", "")
			t.Setenv(name, value)
			if name == "CLASSIFICATION_TIMEOUT_SECONDS" {
				if _, err := ClassificationTimeoutFromEnv(); err == nil {
					t.Fatal("invalid API timeout accepted")
				}
			} else if _, err := NewProvider("decision-1", "none"); err == nil {
				t.Fatal("invalid setting accepted")
			}
		})
	}

	// Partitioning preserves complete option values, including Unicode.
	options := largeOptions()
	batches, err := p.partition(providerState, options, "solicitud")
	if err != nil {
		t.Fatal(err)
	}
	flattened := []Entity{}
	for _, batch := range batches {
		flattened = append(flattened, batch...)
	}
	if !reflect.DeepEqual(options, flattened) {
		t.Fatal("partition lost data")
	}
}
