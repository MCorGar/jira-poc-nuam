package poc

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"time"
)

var ErrProvider = errors.New("proveedor no disponible o respuesta inválida")
var ErrTimeout = errors.New("proveedor timeout")

const abstainCode = "__SIN_COINCIDENCIA__"

type Candidate struct {
	Code  string
	Score float64
}
type Ranking struct {
	Candidates []Candidate
	Model      string
	Stages     []Entity
	Strategy   string
}
type Provider interface {
	Name() string
	Mode() string
	CacheKey() string
	Rank(context.Context, string, []Entity) (Ranking, error)
}

type Policy struct {
	Threshold float64 `json:"umbral"`
	Margin    float64 `json:"margen_minimo"`
}

func (p Policy) normalized() Policy {
	if p == (Policy{}) {
		return Policy{.9, .08}
	}
	return p
}
func (p Policy) Validate() error {
	if math.IsNaN(p.Threshold) || math.IsNaN(p.Margin) || p.Threshold <= 0 || p.Threshold > 1 || p.Margin < 0 || p.Margin > 1 {
		return errors.New("DECISION_THRESHOLD debe estar en (0,1] y DECISION_MIN_MARGIN en [0,1]")
	}
	return nil
}

type MockProvider struct{ Failure string }

func (MockProvider) Name() string       { return "mock-lexical-v3" }
func (MockProvider) Mode() string       { return "simulado" }
func (p MockProvider) CacheKey() string { return p.Name() + ":" + p.Failure }

var stop = func() map[string]bool {
	s := map[string]bool{}
	for _, v := range strings.Fields("de del la el los las y o para por con un una en a al que se servicio gestion solicitud soporte") {
		s[v] = true
	}
	return s
}()

func tokenSet(text string) map[string]bool {
	out := map[string]bool{}
	for _, word := range strings.Fields(Normalize(text)) {
		if !stop[word] {
			out[word] = true
		}
	}
	return out
}
func overlap(a, b map[string]bool) int {
	n := 0
	for k := range a {
		if b[k] {
			n++
		}
	}
	return n
}
func sortCandidates(items []Candidate) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].Score == items[j].Score {
			return items[i].Code < items[j].Code
		}
		return items[i].Score > items[j].Score
	})
}
func (p MockProvider) Rank(ctx context.Context, text string, options []Entity) (Ranking, error) {
	if err := ctx.Err(); err != nil {
		return Ranking{}, err
	}
	if p.Failure == "timeout" {
		return Ranking{}, ErrTimeout
	}
	if p.Failure == "error" {
		return Ranking{}, ErrProvider
	}
	query := tokenSet(text)
	ranked := []Candidate{}
	for _, o := range options {
		name := Normalize(str(o, "name"))
		labels := tokenSet(name)
		score := float64(overlap(query, labels)) / float64(max(1, len(labels)))
		if name != "" && strings.Contains(Normalize(text), name) {
			score = 1
		}
		description := tokenSet(str(o, "description") + " " + str(o, "requirements"))
		score = math.Min(1, score*.95+.05*float64(overlap(query, description))/float64(max(1, len(query))))
		ranked = append(ranked, Candidate{str(o, "code"), math.RoundToEven(score*10000) / 10000})
	}
	sortCandidates(ranked)
	return Ranking{Candidates: ranked, Model: p.Name(), Strategy: "simulada"}, nil
}

// Standard requests retain their original country scopes. Unknown scopes
// remain eligible; known scopes filter options before calling the model.
func requestOptions(c *Catalog, country string) ([]Entity, map[string]Entity, map[string]Entity, error) {
	if c.Schema != CatalogSchema {
		return nil, nil, nil, ErrProvider
	}
	services := map[string]Entity{}
	for _, s := range c.Services {
		code := str(s, "code")
		if code == "" || services[code] != nil || str(s, "name") == "" {
			return nil, nil, nil, ErrProvider
		}
		services[code] = s
	}
	requests := map[string]Entity{}
	options := []Entity{}
	for _, r := range c.Requests {
		code, sid := str(r, "code"), str(r, "parent_code")
		if code == "" || code == abstainCode || requests[code] != nil || str(r, "name") == "" || services[sid] == nil {
			return nil, nil, nil, ErrProvider
		}
		requests[code] = r
		if !countryEligible(stringList(r["countries"]), country) {
			continue
		}
		options = append(options, Entity{"code": code, "name": str(r, "name"), "description": str(r, "description"), "requirements": str(r, "requirements"), "countries": stringList(r["countries"]), "service_name": str(services[sid], "name"), "service_code": sid})
	}
	return options, requests, services, nil
}

// Country scopes are stored verbatim (e.g. Regional, Chile/Perú), not
// replaced with guesses. Unrecognized or absent scopes cannot exclude a request.
func countryEligible(scopes []string, country string) bool {
	if len(scopes) == 0 {
		return true
	}
	target := Normalize(country)
	for _, scope := range scopes {
		normalized := Normalize(scope)
		if normalized == target {
			return true
		}
		if normalized == "regional" {
			if target == "chile" || target == "peru" || target == "colombia" {
				return true
			}
			continue
		}
		known := false
		for _, token := range strings.Fields(normalized) {
			if token == "chile" || token == "peru" || token == "colombia" {
				known = true
				if token == target {
					return true
				}
			}
		}
		if !known {
			return true
		}
	}
	return false
}
func entityRef(e Entity) Entity { return Entity{"codigo": str(e, "code"), "nombre": str(e, "name")} }

func Classify(ctx context.Context, c *Catalog, p Provider, input ClassificationInput, policy Policy) (Result, error) {
	start := time.Now()
	policy = policy.normalized()
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	result := Result{
		"decision_id": newUUID(), "estado": "requiere_aclaracion",
		"motivo": "sin_evidencia_suficiente", "solicitud": nil, "servicio": nil,
		"modo": p.Mode(), "proveedor": p.Name(), "catalogo_version": c.Version, "catalogo_provisional": true,
		"jira_modificado": false, "candidatos": []Entity{}, "reutilizada": false, "politica": policy,
	}
	finish := func(reason string) (Result, error) {
		result["motivo"] = reason
		result["latencia_ms"] = math.Round(float64(time.Since(start).Microseconds())/10) / 100
		return result, nil
	}
	options, requests, services, err := requestOptions(c, input.Country)
	if err != nil {
		return nil, err
	}
	if len(options) == 0 {
		if len(c.Requests) == 0 {
			return finish("catalogo_sin_solicitudes")
		}
		return finish("sin_solicitudes_para_pais")
	}
	stateBytes, _ := json.Marshal(input)
	state := string(stateBytes)
	eligible := map[string]bool{abstainCode: true}
	for _, option := range options {
		eligible[str(option, "code")] = true
	}
	ranking, err := p.Rank(ctx, state, options)
	if err != nil {
		return nil, err
	}
	if len(ranking.Candidates) == 0 {
		return nil, ErrProvider
	}
	ranked := append([]Candidate(nil), ranking.Candidates...)
	seen := map[string]bool{}
	for _, candidate := range ranked {
		if seen[candidate.Code] || !eligible[candidate.Code] || math.IsNaN(candidate.Score) || math.IsInf(candidate.Score, 0) || candidate.Score < 0 || candidate.Score > 1 {
			return nil, ErrProvider
		}
		seen[candidate.Code] = true
	}
	sortCandidates(ranked)
	result["modelo"] = ranking.Model
	result["estrategia"] = ranking.Strategy
	if ranking.Strategy == "" {
		result["estrategia"] = "directa"
	}
	result["etapas"] = ranking.Stages
	if ranking.Stages == nil {
		result["etapas"] = []Entity{}
	}
	candidates := []Entity{}
	for _, candidate := range ranked {
		if candidate.Code == abstainCode || candidate.Score == 0 {
			continue
		}
		request := requests[candidate.Code]
		entry := Entity{"solicitud": entityRef(request), "servicio": entityRef(services[str(request, "parent_code")])}
		if p.Mode() == "simulado" {
			entry["puntaje_simulado"] = candidate.Score
		} else {
			entry["probabilidad"] = candidate.Score
		}
		candidates = append(candidates, entry)
		if len(candidates) == 5 {
			break
		}
	}
	result["candidatos"] = candidates
	if ranked[0].Code == abstainCode {
		return finish("sin_correspondencia_en_catalogo")
	}
	if ranked[0].Score < policy.Threshold {
		return finish("baja_confianza")
	}
	if len(ranked) > 1 && ranked[0].Score-ranked[1].Score < policy.Margin {
		return finish("solicitud_ambigua")
	}
	request := requests[ranked[0].Code]
	// Missing definitions are not suitable for automatic classification yet.
	if str(request, "description") == "" {
		return finish("descripcion_catalogo_pendiente")
	}
	result["estado"] = "clasificado"
	result["solicitud"] = entityRef(request)
	result["servicio"] = entityRef(services[str(request, "parent_code")])
	if p.Mode() == "simulado" {
		result["puntaje_simulado"] = ranked[0].Score
	} else {
		result["probabilidad_solicitud"] = ranked[0].Score
	}
	return finish("solicitud_identificada")
}
