package poc

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/xuri/excelize/v2"
)

var fixtureOnce sync.Once
var fixtureCatalog *Catalog
var fixtureReport map[string]any
var fixtureError error

func imported(t *testing.T) (*Catalog, map[string]any) {
	t.Helper()
	fixtureOnce.Do(func() {
		path := os.Getenv("CATALOG_FILE")
		if path == "" {
			path = "../../../Catálogo de servicio TI 1.xlsx"
		}
		fixtureCatalog, fixtureReport, fixtureError = ImportWorkbook(path)
	})
	if fixtureError != nil {
		t.Fatal(fixtureError)
	}
	return fixtureCatalog, fixtureReport
}

type memoryStore struct {
	mu      sync.Mutex
	c       *Catalog
	report  map[string]any
	results map[string]Result
	fail    bool
}

func memory(t *testing.T) *memoryStore {
	c, r := imported(t)
	return &memoryStore{c: c, report: r, results: map[string]Result{}}
}
func (m *memoryStore) Catalog(context.Context) (*Catalog, map[string]any, error) {
	if m.fail {
		return nil, nil, errors.New("db no disponible")
	}
	return m.c, m.report, nil
}
func clone(r Result) Result {
	if r == nil {
		return nil
	}
	raw, _ := json.Marshal(r)
	var copy Result
	_ = json.Unmarshal(raw, &copy)
	return copy
}
func (m *memoryStore) FindDecision(ctx context.Context, key string) (Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return clone(m.results[key]), nil
}
func (m *memoryStore) SaveDecision(ctx context.Context, key string, r Result) (Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.results[key] == nil {
		m.results[key] = clone(r)
	}
	return clone(m.results[key]), nil
}
func call(h http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestStandardCatalogOnlyApprovedFields(t *testing.T) {
	c, r := imported(t)
	if c.Schema != CatalogSchema || len(c.Requests) != 383 || len(c.Services) != 16 {
		t.Fatal(c.Schema)
	}
	codes := map[string]bool{}
	for _, kind := range GroupOrder {
		for _, e := range c.Groups()[kind] {
			code := str(e, "code")
			if code == "" || codes[code] {
				t.Fatal("código duplicado")
			}
			if parent := str(e, "parent_code"); parent != "" && !codes[parent] {
				t.Fatal("padre inválido")
			}
			codes[code] = true
			allowed := map[string]bool{"code": true, "name": true}
			if kind == "requests" {
				allowed["parent_code"] = true
				allowed["description"] = true
				allowed["requirements"] = true
				allowed["countries"] = true
			}
			if len(e) != len(allowed) {
				t.Fatalf("campos inesperados %s: %v", kind, e)
			}
			for field := range e {
				if !allowed[field] {
					t.Fatal(field)
				}
			}
		}
	}
	raw, _ := json.Marshal(c)
	for _, field := range []string{"families", "variants", "incident_profiles", "incident_policies", "applications", "requester", "priority"} {
		if strings.Contains(string(raw), `"`+field+`":`) {
			t.Fatal("dato excluido persistido", field)
		}
	}
	if !reflect.DeepEqual(r["business_fields"], []string{"servicio", "nombre_solicitud", "descripcion_solicitud", "requisitos", "pais"}) || r["source_sheet"] != standardSheet {
		t.Fatal(r)
	}
}

func minimalWorkbook(t *testing.T, extra string) string {
	t.Helper()
	f := excelize.NewFile()
	defer f.Close()
	if err := f.SetSheetName("Sheet1", standardSheet); err != nil {
		t.Fatal(err)
	}
	rows := [][]any{
		{"Servicio", "Nombre de Solicitud", "Descripcion", "Aplicacion ignorada", "", "", "", "", "", "Requisitos", "Tipo usuario ignorado"},
		{"Accesos", "Crear cuenta", "Alta inicial", extra, "", "", "", "", "", "Carta firmada", "Externo"},
		{"Accesos", "Crear cuenta", "Alta inicial", "otra", "", "", "", "", "", "Carta firmada", "Colaborador"},
		{"Accesos", "Crear cuenta", "Alta adicional", "secreto", "", "", "", "", "", "Aprobación", "Externo"},
		{"Accesos", "Sin descripcion", "Sin informacion", "", "", "", "", "", "", "Documento"},
		{"Aplicaciones", "Crear cuenta", "Otro servicio", "", "", "", "", "", "", "Otro requisito"},
		{"Accesos", "0", "Fila inválida"},
	}
	for i, row := range rows {
		for len(row) < 16 {
			row = append(row, "")
		}
		switch i {
		case 0:
			row[15] = "Pais"
		case 1:
			row[15] = "Chile"
		case 2:
			row[15] = "Peru"
		case 3:
			row[15] = "Regional"
		}
		cell, _ := excelize.CoordinatesToCellName(1, i+1)
		if err := f.SetSheetRow(standardSheet, cell, &row); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(t.TempDir(), "minimal.xlsx")
	if err := f.SaveAs(path); err != nil {
		t.Fatal(err)
	}
	return path
}
func TestImporterNeedsOnlyStandardSheetAndRetainsDistinctRequirements(t *testing.T) {
	first, report, err := ImportWorkbook(minimalWorkbook(t, "informacion a ignorar"))
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Services) != 2 || len(first.Requests) != 3 || report["rows_imported"] != 5 || report["rows_grouped"] != 2 {
		t.Fatal(report)
	}
	request := first.Requests[0]
	if !reflect.DeepEqual(stringList(request["countries"]), []string{"Chile", "Peru", "Regional"}) {
		t.Fatal("países agrupados perdidos", request)
	}
	if str(request, "description") != "Alta inicial\n\nAlta adicional" || str(request, "requirements") != "Carta firmada\n\nAprobación" {
		t.Fatal(request)
	}
	second, _, err := ImportWorkbook(minimalWorkbook(t, "valor totalmente distinto"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.Requests, second.Requests) || !reflect.DeepEqual(first.Services, second.Services) {
		t.Fatal("columna fuera de alcance influye en catálogo")
	}
	if str(first.Requests[0], "code") == str(first.Requests[2], "code") {
		t.Fatal("solicitudes de distintos servicios colapsadas")
	}
}

func TestCatalogReproducibleAndOriginalPreserved(t *testing.T) {
	path := os.Getenv("CATALOG_FILE")
	if path == "" {
		path = "../../../Catálogo de servicio TI 1.xlsx"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	before := sha256.Sum256(data)
	first, _, err := ImportWorkbook(path)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := ImportWorkbook(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("importación no determinista")
	}
	after, _ := os.ReadFile(path)
	if sha256.Sum256(after) != before {
		t.Fatal("Excel modificado")
	}
}

const validInput = `{"titulo":"Crear cuenta","pais":"Chile","tipo_usuario":"Colaborador","descripcion":"Necesito acceso para una persona nueva"}`

func syntheticCatalog() *Catalog {
	return &Catalog{Schema: CatalogSchema, Version: "test-v1", Services: []Entity{{"code": "SER_A", "name": "Accesos"}, {"code": "SER_B", "name": "Aplicaciones"}}, Requests: []Entity{
		{"code": "SOL_A", "name": "Crear cuenta", "description": "Alta de nuevos usuarios", "requirements": "Carta firmada", "parent_code": "SER_A"},
		{"code": "SOL_B", "name": "Ajustar balance", "description": "Recarga de balance", "parent_code": "SER_B"},
	}}
}
func syntheticStore() *memoryStore {
	return &memoryStore{c: syntheticCatalog(), report: map[string]any{}, results: map[string]Result{}}
}

type fixedProvider struct {
	items   []Candidate
	state   string
	options []Entity
	err     error
}

func (*fixedProvider) Name() string     { return "test-decision" }
func (*fixedProvider) Mode() string     { return "real" }
func (*fixedProvider) CacheKey() string { return "test-decision-config" }
func (p *fixedProvider) Rank(_ context.Context, state string, options []Entity) (Ranking, error) {
	p.state = state
	p.options = options
	return Ranking{Candidates: p.items, Model: "test-model"}, p.err
}

func TestClassifiesRequestAndDerivesServiceWithoutVariant(t *testing.T) {
	input, err := DecodeInput([]byte(validInput))
	if err != nil {
		t.Fatal(err)
	}
	p := &fixedProvider{items: []Candidate{{"SOL_A", .94}, {abstainCode, .06}}}
	result, err := Classify(context.Background(), syntheticCatalog(), p, input, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if result["estado"] != "clasificado" || str(result["servicio"].(Entity), "codigo") != "SER_A" || str(result["solicitud"].(Entity), "codigo") != "SOL_A" || result["probabilidad_solicitud"] != .94 {
		t.Fatal(result)
	}
	for _, field := range []string{"variante_codigo", "campos_faltantes", "ticket_id", "tipo_ticket", "perfil_incidente_codigo", "referencia"} {
		if _, exists := result[field]; exists {
			t.Fatal("campo fuera de alcance", field)
		}
	}
	var state ClassificationInput
	if json.Unmarshal([]byte(p.state), &state) != nil || state != input {
		t.Fatalf("contexto no llega al proveedor: %s", p.state)
	}
	if len(p.options) != 2 || str(p.options[0], "code") != "SOL_A" {
		t.Fatal(p.options)
	}
}
func TestClarificationAndInvalidProviderResponses(t *testing.T) {
	input, _ := DecodeInput([]byte(validInput))
	catalog := syntheticCatalog()
	for _, tc := range []struct {
		name   string
		items  []Candidate
		reason string
		fail   bool
		policy Policy
	}{
		{name: "baja", items: []Candidate{{"SOL_A", .6}, {"SOL_B", .4}}, reason: "baja_confianza"},
		{name: "ambigua", items: []Candidate{{"SOL_A", .51}, {"SOL_B", .49}}, reason: "solicitud_ambigua", policy: Policy{.5, .08}},
		{name: "abstencion", items: []Candidate{{abstainCode, .95}, {"SOL_A", .05}}, reason: "sin_correspondencia_en_catalogo"},
		{name: "codigo_inventado", items: []Candidate{{"INVALIDO", .99}}, fail: true},
		{name: "duplicado", items: []Candidate{{"SOL_A", .99}, {"SOL_A", .01}}, fail: true},
		{name: "sin_resultado", fail: true},
		{name: "puntaje_invalido", items: []Candidate{{"SOL_A", 1.1}}, fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := Classify(context.Background(), catalog, &fixedProvider{items: tc.items}, input, tc.policy)
			if tc.fail {
				if err == nil {
					t.Fatal(r)
				}
				return
			}
			if err != nil || r["estado"] != "requiere_aclaracion" || r["motivo"] != tc.reason || r["solicitud"] != nil || r["servicio"] != nil {
				t.Fatalf("%v %v", r, err)
			}
		})
	}
}
func TestCountryAndUserTypeAreContextWithoutInventedFilters(t *testing.T) {
	input, _ := DecodeInput([]byte(validInput))
	p := &fixedProvider{items: []Candidate{{"SOL_A", .95}, {"SOL_B", .05}}}
	c := syntheticCatalog()
	for _, country := range []string{"Chile", "Perú", "Argentina"} {
		input.Country = country
		input.UserType = "Externo"
		result, err := Classify(context.Background(), c, p, input, Policy{})
		if err != nil || result["estado"] != "clasificado" || len(p.options) != 2 {
			t.Fatalf("%v %v", result, err)
		}
		if !strings.Contains(p.state, country) || !strings.Contains(p.state, "Externo") {
			t.Fatal(p.state)
		}
		if str(p.options[0], "requirements") != "Carta firmada" {
			t.Fatal("requisitos no llegan al modelo")
		}
	}
	c.Requests = nil
	result, err := Classify(context.Background(), c, p, input, Policy{})
	if err != nil || result["motivo"] != "catalogo_sin_solicitudes" {
		t.Fatalf("%v %v", result, err)
	}
	c = syntheticCatalog()
	c.Schema = "legacy"
	if _, err := Classify(context.Background(), c, p, input, Policy{}); err == nil {
		t.Fatal("catálogo anterior aceptado")
	}
}

func TestMockNeverClaimsProbability(t *testing.T) {
	input, _ := DecodeInput([]byte(validInput))
	r, err := Classify(context.Background(), syntheticCatalog(), MockProvider{}, input, Policy{})
	if err != nil || r["estado"] != "clasificado" || r["modo"] != "simulado" {
		t.Fatalf("%v %v", r, err)
	}
	if _, ok := r["probabilidad_solicitud"]; ok {
		t.Fatal("probabilidad inventada")
	}
	if _, ok := r["puntaje_simulado"]; !ok {
		t.Fatal("falta puntaje")
	}
}
func TestMissingDescriptionAndBrokenService(t *testing.T) {
	input, _ := DecodeInput([]byte(validInput))
	c := syntheticCatalog()
	p := &fixedProvider{items: []Candidate{{"SOL_A", .99}}}
	c.Requests[0]["description"] = ""
	r, err := Classify(context.Background(), c, p, input, Policy{})
	if err != nil || r["motivo"] != "descripcion_catalogo_pendiente" {
		t.Fatalf("%v %v", r, err)
	}
	c.Requests[0]["parent_code"] = "DESCONOCIDO"
	if _, err := Classify(context.Background(), c, p, input, Policy{}); err == nil {
		t.Fatal("servicio inexistente")
	}
}
func TestValidation(t *testing.T) {
	h := (&API{Store: syntheticStore(), Provider: MockProvider{}}).Handler()
	invalid := []string{`{}`, `null`, `[]`, validInput + ` {}`, `{"titulo":"x","pais":"Chile","tipo_usuario":"Colaborador"}`, `{"ticket_id":"1","tipo_ticket":"requerimiento","resumen":"x"}`}
	for _, field := range []string{"titulo", "pais", "tipo_usuario", "descripcion"} {
		var body map[string]any
		_ = json.Unmarshal([]byte(validInput), &body)
		for _, value := range []any{nil, "", "  ", 4, []string{"x"}} {
			body[field] = value
			raw, _ := json.Marshal(body)
			invalid = append(invalid, string(raw))
		}
		delete(body, field)
		raw, _ := json.Marshal(body)
		invalid = append(invalid, string(raw))
	}
	for _, field := range []string{"referencia", "contexto", "aplicacion", "ambiente"} {
		invalid = append(invalid, strings.TrimSuffix(validInput, "}")+`,"`+field+`":"x"}`)
	}
	for _, body := range invalid {
		if rr := call(h, "POST", "/clasificaciones", body, ""); rr.Code != 422 {
			t.Fatalf("%s: %d %s", body, rr.Code, rr.Body.String())
		}
	}
	for field, limit := range map[string]int{"titulo": 1000, "pais": 80, "tipo_usuario": 100, "descripcion": 20000} {
		var body map[string]any
		_ = json.Unmarshal([]byte(validInput), &body)
		body[field] = strings.Repeat("á", limit+1)
		raw, _ := json.Marshal(body)
		if rr := call(h, "POST", "/clasificaciones", string(raw), ""); rr.Code != 422 {
			t.Fatal(field, rr.Code)
		}
	}
	if rr := call(h, "POST", "/clasificaciones", strings.Repeat("x", 129<<10), ""); rr.Code != 413 {
		t.Fatal(rr.Code)
	}
}
func TestIdempotencyConcurrentAndChangedInputs(t *testing.T) {
	store := syntheticStore()
	h := (&API{Store: store, Provider: MockProvider{}}).Handler()
	ids := make(chan string, 12)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rr := call(h, "POST", "/clasificaciones", validInput, "")
			var body Result
			_ = json.Unmarshal(rr.Body.Bytes(), &body)
			if rr.Code != 200 {
				t.Errorf("%d %s", rr.Code, rr.Body.String())
				return
			}
			ids <- body["decision_id"].(string)
		}()
	}
	wg.Wait()
	close(ids)
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if first != id {
			t.Fatal("decisiones duplicadas")
		}
	}
	for _, change := range [][2]string{{"Crear cuenta", "Crear cuenta urgente"}, {"Chile", "Perú"}, {"Colaborador", "Externo"}, {"persona nueva", "persona nueva del área"}} {
		rr := call(h, "POST", "/clasificaciones", strings.Replace(validInput, change[0], change[1], 1), "")
		var body Result
		_ = json.Unmarshal(rr.Body.Bytes(), &body)
		if rr.Code != 200 || body["decision_id"] == first {
			t.Fatalf("entrada nueva reutilizada: %s", rr.Body.String())
		}
	}
	input, _ := DecodeInput([]byte(validInput))
	key := IdempotencyKey(input, "v1", "p1", Policy{.9, .08})
	for _, other := range []string{IdempotencyKey(input, "v2", "p1", Policy{.9, .08}), IdempotencyKey(input, "v1", "p2", Policy{.9, .08}), IdempotencyKey(input, "v1", "p1", Policy{.95, .08})} {
		if key == other {
			t.Fatal("configuración reutilizada")
		}
	}
}
func TestProviderErrorsNotPersisted(t *testing.T) {
	for failure, status := range map[string]int{"timeout": 504, "error": 503} {
		t.Run(failure, func(t *testing.T) {
			store := syntheticStore()
			h := (&API{Store: store, Provider: MockProvider{Failure: failure}}).Handler()
			rr := call(h, "POST", "/clasificaciones", validInput, "")
			if rr.Code != status || len(store.results) != 0 {
				t.Fatalf("%d %s", rr.Code, rr.Body.String())
			}
		})
	}
}
func TestAuthReadinessAndDocs(t *testing.T) {
	store := syntheticStore()
	h := (&API{Store: store, Provider: MockProvider{}, Token: "secret"}).Handler()
	for _, path := range []string{"/health", "/ready", "/docs", "/openapi.json"} {
		if rr := call(h, "GET", path, "", ""); rr.Code != 200 {
			t.Fatal(path, rr.Code)
		}
	}
	if call(h, "POST", "/clasificaciones", validInput, "").Code != 401 {
		t.Fatal("sin auth")
	}
	if call(h, "POST", "/clasificaciones", validInput, "secret").Code != 200 {
		t.Fatal("auth inválida")
	}
	store.c = nil
	if call(h, "GET", "/ready", "", "").Code != 503 {
		t.Fatal("catálogo ausente")
	}
	store.fail = true
	if call(h, "GET", "/ready", "", "").Code != 503 {
		t.Fatal("base caída")
	}
}
func TestCatalogPaginationAndFilters(t *testing.T) {
	h := (&API{Store: memory(t), Provider: MockProvider{}}).Handler()
	for _, path := range []string{"/catalogo/solicitudes?limit=0", "/catalogo/solicitudes?offset=-1", "/catalogo/solicitudes?offset=bad"} {
		if call(h, "GET", path, "", "").Code != 422 {
			t.Fatal(path)
		}
	}
	rr := call(h, "GET", "/catalogo/solicitudes?q=Crear%20Cuenta&limit=1", "", "")
	var body map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	if rr.Code != 200 || body["total"] != float64(5) || len(body["items"].([]any)) != 1 {
		t.Fatal(rr.Code, body)
	}
	for _, path := range []string{"/catalogo/familias", "/catalogo/variantes", "/catalogo/perfiles-incidente", "/catalogo/inventado"} {
		if call(h, "GET", path, "", "").Code != 404 {
			t.Fatal("recurso inexistente")
		}
	}
}
func TestManualContractCases(t *testing.T) {
	raw, err := os.ReadFile("../../examples/manual_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			ID       string          `json:"id"`
			Input    json.RawMessage `json:"input"`
			Status   int             `json:"expected_status"`
			Expected map[string]any  `json:"expected"`
		} `json:"cases"`
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, c := range fixture.Cases {
		t.Run(c.ID, func(t *testing.T) {
			rr := call((&API{Store: memory(t), Provider: MockProvider{}}).Handler(), "POST", "/clasificaciones", string(c.Input), "")
			if rr.Code != c.Status {
				t.Fatalf("%d %s", rr.Code, rr.Body.String())
			}
			var got map[string]any
			_ = json.Unmarshal(rr.Body.Bytes(), &got)
			for k, v := range c.Expected {
				if !reflect.DeepEqual(got[k], v) {
					t.Fatalf("%s: %v != %v; %s", k, got[k], v, rr.Body.String())
				}
			}
		})
	}
}
func TestEmptyOrMissingWorkbook(t *testing.T) {
	if _, _, err := ImportWorkbook(filepath.Join(t.TempDir(), "missing.xlsx")); err == nil {
		t.Fatal("archivo ausente")
	}
	if Normalize(" CIBERSEGURIDAD y Ácceso ") != "ciberseguridad y acceso" {
		t.Fatal("normalización")
	}
}

func TestSameRequestNameAcrossServicesRequiresClarification(t *testing.T) {
	c := syntheticCatalog()
	c.Requests[1]["name"] = "Crear cuenta"
	input, _ := DecodeInput([]byte(validInput))
	result, err := Classify(context.Background(), c, MockProvider{}, input, Policy{})
	if err != nil || result["estado"] != "requiere_aclaracion" || result["motivo"] != "solicitud_ambigua" {
		t.Fatalf("%v %v", result, err)
	}
	candidates := result["candidatos"].([]Entity)
	if len(candidates) != 2 || str(candidates[0]["servicio"].(Entity), "codigo") == str(candidates[1]["servicio"].(Entity), "codigo") {
		t.Fatal("solicitudes homónimas colapsadas", candidates)
	}
}

func TestCountryScopesAndFiltering(t *testing.T) {
	for _, tc := range []struct {
		scopes  []string
		country string
		want    bool
	}{
		{[]string{"Chile"}, "Chile", true}, {[]string{"Chile"}, "Perú", false},
		{[]string{"Chile/Perú"}, "Peru", true}, {[]string{"Chile - Colombia"}, "Perú", false},
		{[]string{"Regional"}, "Colombia", true}, {[]string{"Regional"}, "Argentina", false},
		{[]string{"Chile", "Peru"}, "Perú", true}, {[]string{"Chile", ""}, "Perú", true},
		{[]string{"0"}, "Chile", true}, {[]string{"desconocido"}, "Chile", true}, {nil, "Chile", true},
	} {
		if got := countryEligible(tc.scopes, tc.country); got != tc.want {
			t.Fatalf("%v %s: %v", tc.scopes, tc.country, got)
		}
	}
	c := syntheticCatalog()
	c.Requests[0]["countries"] = []string{"Chile"}
	c.Requests[1]["countries"] = []string{"Peru"}
	input, _ := DecodeInput([]byte(validInput))
	p := &fixedProvider{items: []Candidate{{"SOL_A", .95}, {abstainCode, .05}}}
	r, err := Classify(context.Background(), c, p, input, Policy{})
	if err != nil || r["estado"] != "clasificado" || len(p.options) != 1 || str(p.options[0], "code") != "SOL_A" {
		t.Fatalf("%v %v", r, err)
	}
	if !reflect.DeepEqual(stringList(p.options[0]["countries"]), []string{"Chile"}) {
		t.Fatal("país no llega a la IA")
	}
	// Filtering also holds for JSONB-decoded country arrays.
	raw, _ := json.Marshal(c)
	var decoded Catalog
	_ = json.Unmarshal(raw, &decoded)
	input.Country = "Argentina"
	r, err = Classify(context.Background(), &decoded, p, input, Policy{})
	if err != nil || r["motivo"] != "sin_solicitudes_para_pais" {
		t.Fatalf("%v %v", r, err)
	}
	input.Country = "Peru"
	if _, err := Classify(context.Background(), c, p, input, Policy{}); err == nil {
		t.Fatal("respuesta del proveedor fuera del país aceptada")
	}
}
