package poc

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xuri/excelize/v2"
)

const standardSheet = "Matriz Solicitudes Estándar"

// Only the five approved business fields are imported. Repeated service/name
// rows are grouped, retaining every distinct description and requirement.
func ImportWorkbook(path string) (*Catalog, map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	hash := sha256.Sum256(data)
	sourceHash := hex.EncodeToString(hash[:])
	f, err := excelize.OpenFile(path, excelize.Options{RawCellValue: true, UnzipSizeLimit: 64 << 20, UnzipXMLSizeLimit: 32 << 20})
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	rows, err := f.GetRows(standardSheet)
	if err != nil {
		return nil, nil, fmt.Errorf("no se pudo leer %s: %w", standardSheet, err)
	}
	val := func(row []string, col int) string {
		if col >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[col])
	}
	c := &Catalog{Schema: CatalogSchema, Version: "excel-" + sourceHash[:16] + "-" + CatalogSchema, Services: []Entity{}, Requests: []Entity{}}
	services, requests := map[string]Entity{}, map[string]Entity{}
	descriptions, requirements := map[string][]string{}, map[string][]string{}
	countries := map[string][]string{}
	issues := []Entity{}
	accepted := 0
	appendUnique := func(values []string, value string) []string {
		for _, existing := range values {
			if existing == value {
				return values
			}
		}
		return append(values, value)
	}
	for index, row := range rows {
		if index == 0 {
			continue
		}
		serviceName, name, description, requirement := val(row, 0), val(row, 1), val(row, 2), val(row, 9)
		country := val(row, 15)
		if serviceName == "" && name == "" && description == "" && requirement == "" {
			continue
		}
		if serviceName == "" || name == "" || Normalize(name) == "0" || Normalize(name) == "false" || Normalize(serviceName) == "0" || Normalize(serviceName) == "false" {
			issues = append(issues, Entity{"row": index + 1, "issue": "fila_invalida_excluida"})
			continue
		}
		switch Normalize(description) {
		case "", "sin informacion", "n a":
			description = ""
			issues = append(issues, Entity{"row": index + 1, "issue": "descripcion_pendiente"})
		}
		sid := Code("SER", serviceName)
		if services[sid] == nil {
			service := Entity{"code": sid, "name": serviceName}
			services[sid] = service
			c.Services = append(c.Services, service)
		}
		rid := Code("SOL", sid, name)
		if requests[rid] == nil {
			request := Entity{"code": rid, "parent_code": sid, "name": name, "description": "", "requirements": "", "countries": []string{}}
			requests[rid] = request
			c.Requests = append(c.Requests, request)
		}
		if description != "" {
			descriptions[rid] = appendUnique(descriptions[rid], description)
		}
		if requirement != "" {
			requirements[rid] = appendUnique(requirements[rid], requirement)
		}
		countries[rid] = appendUnique(countries[rid], country)
		accepted++
	}
	for _, r := range c.Requests {
		rid := str(r, "code")
		r["description"] = strings.Join(descriptions[rid], "\n\n")
		r["requirements"] = strings.Join(requirements[rid], "\n\n")
		r["countries"] = countries[rid]
	}
	report := map[string]any{
		"source_file": filepath.Base(path), "source_sha256": sourceHash, "catalog_version": c.Version, "schema": CatalogSchema,
		"source_sheet": standardSheet, "business_fields": []string{"servicio", "nombre_solicitud", "descripcion_solicitud", "requisitos", "pais"},
		"counts":        map[string]int{"services": len(c.Services), "requests": len(c.Requests)},
		"rows_imported": accepted, "rows_grouped": accepted - len(c.Requests), "issues": issues,
		"pending":     []string{"Confirmar vigencia y agrupación con el responsable", "Completar descripciones pendientes", "Obtener IDs de Jira"},
		"provisional": true,
	}
	return c, report, nil
}
