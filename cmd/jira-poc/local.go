package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
)

func randomHex() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func writeFile(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0600)
}
func prepareLocal(args []string) error {
	flags := flag.NewFlagSet("prepare-local", flag.ContinueOnError)
	file := flags.String("file", ".runtime/secrets.env", "Archivo de secretos")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*file), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(*file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	password := randomHex()
	_, err = fmt.Fprintf(f, "POSTGRES_PASSWORD=%s\nDATABASE_URL=postgresql://jira_poc:%s@postgres:5432/jira_poc\nAPI_TOKEN=%s\n", password, password, randomHex())
	return err
}
func postmanEnv(args []string) error {
	flags := flag.NewFlagSet("postman-env", flag.ContinueOnError)
	file := flags.String("file", ".runtime/postman.local.environment.json", "Destino")
	if err := flags.Parse(args); err != nil {
		return err
	}
	var secret struct {
		Data map[string]string `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(os.Stdin, 1<<20)).Decode(&secret); err != nil {
		return errors.New("Secret de Kubernetes inválido")
	}
	token, err := base64.StdEncoding.DecodeString(secret.Data["API_TOKEN"])
	if err != nil || len(token) == 0 {
		return errors.New("falta API_TOKEN en el Secret")
	}
	return writeFile(*file, map[string]any{"name": "Jira PoC local", "values": []map[string]any{{"key": "base_url", "value": "http://127.0.0.1:8000", "enabled": true}, {"key": "api_token", "value": string(token), "enabled": true, "type": "secret"}}, "_postman_variable_scope": "environment"})
}
func importJob(args []string) error {
	flags := flag.NewFlagSet("import-job", flag.ContinueOnError)
	name := flags.String("name", "", "Nombre del Job")
	file := flags.String("file", "", "Excel")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *name == "" || *file == "" {
		return errors.New("se requiere --name y --file")
	}
	filename := filepath.Base(*file)
	job := map[string]any{"apiVersion": "batch/v1", "kind": "Job", "metadata": map[string]any{"name": *name, "namespace": "jira-poc"}, "spec": map[string]any{"backoffLimit": 0, "activeDeadlineSeconds": 150, "ttlSecondsAfterFinished": 3600, "template": map[string]any{"spec": map[string]any{"restartPolicy": "Never", "containers": []any{map[string]any{"name": "import", "image": "jira-poc-nuam:local", "imagePullPolicy": "Never", "command": []string{"/jira-poc", "import-catalog", "--file", "/catalog/" + filename}, "resources": map[string]any{"requests": map[string]string{"cpu": "100m", "memory": "96Mi"}, "limits": map[string]string{"cpu": "500m", "memory": "256Mi"}}, "env": []any{map[string]any{"name": "DATABASE_URL", "valueFrom": map[string]any{"secretKeyRef": map[string]string{"name": "jira-poc-secrets", "key": "DATABASE_URL"}}}}, "volumeMounts": []any{map[string]any{"name": "catalog", "mountPath": "/catalog", "readOnly": true}}}}, "volumes": []any{map[string]any{"name": "catalog", "configMap": map[string]any{"name": *name, "items": []any{map[string]string{"key": "catalog.xlsx", "path": filename}}}}}}}}}
	return output(job)
}

type manualCase struct {
	ID       string          `json:"id"`
	Input    json.RawMessage `json:"input"`
	Status   int             `json:"expected_status"`
	Expected map[string]any  `json:"expected"`
}
type manualFixture struct {
	Reviewed bool         `json:"human_reviewed"`
	Cases    []manualCase `json:"cases"`
}

func loadFixture(path string) (manualFixture, error) {
	var f manualFixture
	raw, err := os.ReadFile(path)
	if err != nil {
		return f, err
	}
	err = json.Unmarshal(raw, &f)
	if err == nil && len(f.Cases) == 0 {
		err = errors.New("el conjunto no contiene casos")
	}
	return f, err
}
func evaluate(args []string) error {
	flags := flag.NewFlagSet("evaluate", flag.ContinueOnError)
	cases := flags.String("cases", "examples/manual_cases.json", "Casos")
	environment := flags.String("environment", ".runtime/postman.local.environment.json", "Entorno Postman")
	dest := flags.String("output", "data/reports/smoke.json", "Reporte")
	if err := flags.Parse(args); err != nil {
		return err
	}
	fixture, err := loadFixture(*cases)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(*environment)
	if err != nil {
		return err
	}
	var env struct {
		Values []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"values"`
	}
	if err = json.Unmarshal(raw, &env); err != nil {
		return err
	}
	values := map[string]string{}
	for _, v := range env.Values {
		values[v.Key] = v.Value
	}
	client := http.Client{Timeout: 70 * time.Second}
	results := []map[string]any{}
	latencies := []float64{}
	states := map[string]int{}
	modes := map[string]bool{}
	passed := 0
	for _, c := range fixture.Cases {
		start := time.Now()
		req, err := http.NewRequest(http.MethodPost, strings.TrimRight(values["base_url"], "/")+"/clasificaciones", strings.NewReader(string(c.Input)))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+values["api_token"])
		req.Header.Set("Content-Type", "application/json")
		response, err := client.Do(req)
		entry := map[string]any{"case": c.ID, "passed": false}
		var input map[string]any
		_ = json.Unmarshal(c.Input, &input)
		entry["pais"] = input["pais"]
		entry["tipo_usuario"] = input["tipo_usuario"]
		if err != nil {
			entry["error"] = err.Error()
			states["error"]++
		} else {
			var body map[string]any
			decodeErr := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&body)
			response.Body.Close()
			ok := decodeErr == nil && response.StatusCode == c.Status
			for key, value := range c.Expected {
				if !reflect.DeepEqual(body[key], value) {
					ok = false
				}
			}
			entry["status"] = response.StatusCode
			entry["result"] = body
			entry["passed"] = ok
			if ok {
				passed++
			}
			if mode, ok := body["modo"].(string); ok {
				modes[mode] = true
			}
			state, _ := body["estado"].(string)
			if state == "" {
				state = "error"
			}
			states[state]++
		}
		latency := float64(time.Since(start).Microseconds()) / 1000
		entry["latency_ms"] = latency
		latencies = append(latencies, latency)
		results = append(results, entry)
	}
	sort.Float64s(latencies)
	median := latencies[len(latencies)/2]
	if len(latencies)%2 == 0 {
		median = (median + latencies[len(latencies)/2-1]) / 2
	}
	mode := "sin_respuestas"
	if len(modes) == 1 {
		for value := range modes {
			mode = value
		}
	} else if len(modes) > 1 {
		mode = "mixto"
	}
	summary := map[string]any{"mode": mode, "real_model_accuracy_measured": false, "human_reviewed": fixture.Reviewed, "cases": len(results), "passed": passed, "states": states, "latency_p50_ms": median, "results": results}
	if err = writeFile(*dest, summary); err != nil {
		return err
	}
	delete(summary, "results")
	if err = output(summary); err != nil {
		return err
	}
	if passed != len(results) {
		return errors.New("hay casos que no cumplen las expectativas; revisar el reporte")
	}
	return nil
}

func generatePostman(args []string) error {
	flags := flag.NewFlagSet("generate-postman", flag.ContinueOnError)
	file := flags.String("cases", "examples/manual_cases.json", "Casos")
	dest := flags.String("output", "postman/collection.json", "Colección")
	if err := flags.Parse(args); err != nil {
		return err
	}
	fixture, err := loadFixture(*file)
	if err != nil {
		return err
	}
	items := []any{}
	event := func(lines []string) []any {
		return []any{map[string]any{"listen": "test", "script": map[string]any{"type": "text/javascript", "exec": lines}}}
	}
	for _, path := range []string{"health", "ready", "catalogo/resumen", "catalogo/solicitudes?q=Crear%20Cuenta"} {
		items = append(items, map[string]any{"name": path, "request": map[string]any{"method": "GET", "url": "{{base_url}}/" + path}, "event": event([]string{`pm.test("HTTP 200", () => pm.response.to.have.status(200));`})})
	}
	for _, c := range fixture.Cases {
		lines := []string{fmt.Sprintf(`pm.test("HTTP esperado", () => pm.response.to.have.status(%d));`, c.Status)}
		if len(c.Expected) > 0 {
			lines = append(lines, "const body = pm.response.json();")
			keys := []string{}
			for k := range c.Expected {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				key, _ := json.Marshal(k)
				value, _ := json.Marshal(c.Expected[k])
				lines = append(lines, fmt.Sprintf("pm.test(%s, () => pm.expect(body[%s]).to.eql(%s));", key, key, value))
			}
		}
		items = append(items, map[string]any{"name": c.ID, "request": map[string]any{"method": "POST", "url": "{{base_url}}/clasificaciones", "header": []any{map[string]string{"key": "Content-Type", "value": "application/json"}}, "body": map[string]any{"mode": "raw", "raw": string(c.Input)}}, "event": event(lines)})
	}
	return writeFile(*dest, map[string]any{"info": map[string]any{"name": "Jira nuam: clasificación de solicitud y servicio", "schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json", "description": "Entrada: titulo, pais, tipo_usuario y descripcion. Casos para el simulador; no miden precisión de Decision-1."}, "auth": map[string]any{"type": "bearer", "bearer": []any{map[string]string{"key": "token", "value": "{{api_token}}", "type": "string"}}}, "item": items})
}
