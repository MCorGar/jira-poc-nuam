package poc

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

type Entity map[string]any

const CatalogSchema = "standard-requests-v3"

type Catalog struct {
	Schema   string   `json:"schema"`
	Version  string   `json:"version"`
	Services []Entity `json:"services"`
	Requests []Entity `json:"requests"`
}

func (c *Catalog) Groups() map[string][]Entity {
	return map[string][]Entity{"services": c.Services, "requests": c.Requests}
}

var GroupOrder = []string{"services", "requests"}

func str(e Entity, k string) string { v, _ := e[k].(string); return v }
func stringList(value any) []string {
	if items, ok := value.([]string); ok {
		return items
	}
	result := []string{}
	if items, ok := value.([]any); ok {
		for _, item := range items {
			if text, ok := item.(string); ok {
				result = append(result, text)
			}
		}
	}
	return result
}
func Normalize(s string) string {
	var b strings.Builder
	for _, r := range norm.NFKD.String(cases.Fold().String(s)) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
func Code(prefix string, parts ...string) string {
	for i := range parts {
		parts[i] = Normalize(parts[i])
	}
	h := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return prefix + "_" + hex.EncodeToString(h[:8])
}
func newUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// ClassificationInput describes a draft, before a Jira ticket exists.
type ClassificationInput struct {
	Country     string `json:"pais"`
	UserType    string `json:"tipo_usuario"`
	Title       string `json:"titulo"`
	Description string `json:"descripcion"`
}

func (t *ClassificationInput) Validate() error {
	for _, f := range []struct {
		p        *string
		max      int
		required bool
	}{
		{&t.Title, 1000, true}, {&t.Country, 80, true}, {&t.UserType, 100, true}, {&t.Description, 20000, true},
	} {
		*f.p = strings.TrimSpace(*f.p)
		if !utf8.ValidString(*f.p) || utf8.RuneCountInString(*f.p) > f.max || (f.required && *f.p == "") {
			return fmt.Errorf("titulo, pais, tipo_usuario y descripcion son obligatorios; verificar tipos y longitudes")
		}
	}
	return nil
}

// The new namespace prevents reuse of decisions made by the former ticket/variant flow.
func IdempotencyKey(t ClassificationInput, version, providerConfig string, policy Policy) string {
	raw, _ := json.Marshal(struct {
		Input    ClassificationInput
		Catalog  string
		Provider string
		Policy   Policy
		Contract string
	}{t, version, providerConfig, policy, "standard-request-service-hierarchy-v4"})
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}
